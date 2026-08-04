// Dish explanations via Claude.
//
// The menu only gives a dish's name, which is often a bare list of ingredients
// ("Tarteletter med høns i asparges"). With -explain we ask Claude for a one-
// sentence description of each dish and print it under the name.
//
// There are two ways to reach Claude, tried in this order:
//
//  1. The `claude` CLI (Claude Code) in non-interactive mode, which runs on a
//     Claude subscription. Preferred, because it needs no API key.
//  2. The Anthropic API, for anyone who has a key but not Claude Code.
//
// Either way the dishes are explained in concurrent batches, and the answers
// are cached on disk so re-running the tool during the week costs nothing.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
)

// Models differ per backend: the CLI takes an alias and bills against the
// subscription, the API takes a full model id and bills per token.
const (
	explainCLIModel = "sonnet"
	explainAPIModel = "claude-opus-5"
)

// Dishes are explained in concurrent batches. Writing the sentences is what
// takes the time, so several short lists finish sooner than one long one;
// the cap keeps us from launching a dozen Claude Code sessions at once.
const (
	explainChunkSize   = 6
	explainMaxParallel = 4
)

const explainSystemDA = `Du forklarer frokostretter for en kantinemenu.

Du får en nummereret liste af retter. Skriv for hver ret én kort sætning på
dansk (maks. 25 ord), der forklarer hvad retten er: tilberedning, oprindelse
eller de ingredienser der ikke fremgår af navnet. Gentag ikke bare navnet.
Hvis en ret er ukendt, så beskriv hvad den sandsynligvis er.`

const explainSystemEN = `You explain lunch dishes for a canteen menu.

You get a numbered list of dishes. For each one, write a single short sentence
in English (max 25 words) explaining what the dish is: how it is prepared, where
it comes from, or the ingredients its name doesn't reveal. Don't just restate
the name. If a dish is unfamiliar, describe what it most likely is.`

// explainRequest is the JSON schema we constrain the response to. Indices refer
// back to the numbered list we send, so a reordered answer still lines up.
var explainSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"explanations": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"index":       map[string]any{"type": "integer"},
					"explanation": map[string]any{"type": "string"},
				},
				"required":             []string{"index", "explanation"},
				"additionalProperties": false,
			},
		},
	},
	"required":             []string{"explanations"},
	"additionalProperties": false,
}

// explainDishes returns a name -> explanation map for the given dishes. Dishes
// already in the on-disk cache are answered from there; the rest go to Claude
// in one request. A dish is simply absent from the map if we couldn't explain
// it.
func explainDishes(ctx context.Context, dishes []string, lang string) (map[string]string, error) {
	cache := loadExplanationCache()

	out := make(map[string]string, len(dishes))
	var missing []string
	for _, name := range dishes {
		if exp, ok := cache[cacheKey(name, lang)]; ok {
			out[name] = exp
			continue
		}
		missing = append(missing, name)
	}
	if len(missing) == 0 {
		return out, nil
	}

	// Asking Claude takes a while, and until it answers we have nothing to
	// print. Say so, or the tool looks like it has hung.
	fmt.Fprintf(os.Stderr, "Explaining %d new dishes (only needed once per menu) ...\n", len(missing))

	// A partial answer is still worth keeping: one failed chunk shouldn't throw
	// away the ones that succeeded.
	fresh, err := requestExplanations(ctx, missing, lang)
	for name, exp := range fresh {
		out[name] = exp
		cache[cacheKey(name, lang)] = exp
	}
	if len(fresh) > 0 {
		saveExplanationCache(cache)
	}
	return out, err
}

// requestExplanations explains every dish, splitting the work into chunks that
// run concurrently. Generating the sentences dominates the wall clock, so
// asking for a few short lists in parallel beats one long list by a wide
// margin.
func requestExplanations(ctx context.Context, dishes []string, lang string) (map[string]string, error) {
	system := explainSystemDA
	if lang == "en" {
		system = explainSystemEN
	}
	ask := pickBackend()

	chunks := chunkDishes(dishes, explainChunkSize)
	results := make([]map[string]string, len(chunks))
	errs := make([]error, len(chunks))

	sem := make(chan struct{}, explainMaxParallel)
	var wg sync.WaitGroup
	for i, chunk := range chunks {
		wg.Add(1)
		go func(i int, chunk []string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i], errs[i] = explainChunk(ctx, ask, system, chunk)
		}(i, chunk)
	}
	wg.Wait()

	merged := make(map[string]string, len(dishes))
	for _, r := range results {
		for name, exp := range r {
			merged[name] = exp
		}
	}
	return merged, errors.Join(errs...)
}

// explainChunk asks for one batch of dishes. Indices in the prompt are local to
// the chunk, so each batch is self-contained.
func explainChunk(ctx context.Context, ask explainBackend, system string, dishes []string) (map[string]string, error) {
	var list strings.Builder
	for i, name := range dishes {
		fmt.Fprintf(&list, "%d. %s\n", i, name)
	}
	answer, err := ask(ctx, system, list.String())
	if err != nil {
		return nil, err
	}
	return parseExplanations(answer, dishes)
}

func chunkDishes(dishes []string, size int) [][]string {
	var chunks [][]string
	for start := 0; start < len(dishes); start += size {
		end := min(start+size, len(dishes))
		chunks = append(chunks, dishes[start:end])
	}
	return chunks
}

// explainBackend turns a system prompt plus a dish list into a raw JSON answer.
type explainBackend func(ctx context.Context, system, prompt string) (string, error)

// pickBackend resolves how to reach Claude once, rather than per chunk,
// preferring the `claude` CLI so that a Claude subscription is enough.
func pickBackend() explainBackend {
	if bin, err := exec.LookPath("claude"); err == nil {
		return func(ctx context.Context, system, prompt string) (string, error) {
			return askClaudeCLI(ctx, bin, system, prompt)
		}
	}
	return askClaudeAPI
}

// askClaudeCLI shells out to Claude Code's non-interactive mode, which uses
// whatever credentials Claude Code is already logged in with — including a
// Claude subscription, so no API key is needed.
func askClaudeCLI(ctx context.Context, bin, system, prompt string) (string, error) {
	schema, err := json.Marshal(explainSchema)
	if err != nil {
		return "", err
	}

	cmd := exec.CommandContext(ctx, bin,
		"--print",
		"--model", explainCLIModel,
		"--effort", "low", // one sentence per dish; no need to deliberate
		"--allowed-tools", "", // pure text task; skip loading the tool harness
		"--output-format", "text",
		"--system-prompt", system,
		"--json-schema", string(schema),
		prompt,
	)
	// Run outside the project so Claude Code doesn't pick up CLAUDE.md or other
	// repo context that has nothing to do with lunch.
	cmd.Dir = os.TempDir()

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", fmt.Errorf("claude: %s", msg)
		}
		return "", fmt.Errorf("running claude: %w", err)
	}
	return stdout.String(), nil
}

func askClaudeAPI(ctx context.Context, system, prompt string) (string, error) {
	client := anthropic.NewClient()
	resp, err := client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     explainAPIModel,
		MaxTokens: 8000,
		System:    []anthropic.TextBlockParam{{Text: system}},
		OutputConfig: anthropic.OutputConfigParam{
			// The task is a one-liner per dish; low effort keeps it quick.
			Effort: anthropic.OutputConfigEffortLow,
			Format: anthropic.JSONOutputFormatParam{Schema: explainSchema},
		},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(prompt)),
		},
	})
	if err != nil {
		return "", explainError(err)
	}

	var text strings.Builder
	for _, block := range resp.Content {
		if t, ok := block.AsAny().(anthropic.TextBlock); ok {
			text.WriteString(t.Text)
		}
	}
	return text.String(), nil
}

// parseExplanations maps the model's answer back onto dish names. Indices refer
// to the numbered list we sent, so out-of-range ones are simply dropped.
func parseExplanations(answer string, dishes []string) (map[string]string, error) {
	var parsed struct {
		Explanations []struct {
			Index       int    `json:"index"`
			Explanation string `json:"explanation"`
		} `json:"explanations"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(answer)), &parsed); err != nil {
		return nil, fmt.Errorf("parsing explanations: %w", err)
	}

	out := make(map[string]string, len(parsed.Explanations))
	for _, e := range parsed.Explanations {
		if e.Index < 0 || e.Index >= len(dishes) {
			continue
		}
		if exp := strings.TrimSpace(e.Explanation); exp != "" {
			out[dishes[e.Index]] = exp
		}
	}
	return out, nil
}

// explainError adds a hint about credentials when the API rejects our key,
// which is by far the most likely first-run failure.
func explainError(err error) error {
	var apiErr *anthropic.Error
	if errors.As(err, &apiErr) && (apiErr.StatusCode == 401 || apiErr.StatusCode == 403) {
		return fmt.Errorf("%w — set ANTHROPIC_API_KEY to a key from https://console.anthropic.com", err)
	}
	return err
}

// --- cache -------------------------------------------------------------

func cacheKey(name, lang string) string { return lang + "\x00" + name }

func explanationCachePath() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, binName, "explanations.json")
}

func loadExplanationCache() map[string]string {
	cache := map[string]string{}
	path := explanationCachePath()
	if path == "" {
		return cache
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return cache
	}
	if err := json.Unmarshal(data, &cache); err != nil {
		return map[string]string{}
	}
	return cache
}

// saveExplanationCache writes the cache back, ignoring errors — a missing cache
// only costs an extra API call next time.
func saveExplanationCache(cache map[string]string) {
	path := explanationCachePath()
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	data, err := json.Marshal(cache)
	if err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0o644)
}

// explainTimeout bounds the whole explanation round-trip so a hanging request
// can't hold the menu hostage. Generous because the CLI backend spawns a whole
// Claude Code session: a full two-week menu takes ~30s.
const explainTimeout = 3 * time.Minute
