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
// Either way all dishes on screen go out in a single request, and the answers
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
	"time"

	"github.com/anthropics/anthropic-sdk-go"
)

// Models differ per backend: the CLI takes an alias and bills against the
// subscription, the API takes a full model id and bills per token.
const (
	explainCLIModel = "sonnet"
	explainAPIModel = "claude-opus-5"
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

	fresh, err := requestExplanations(ctx, missing, lang)
	if err != nil {
		// Whatever came from the cache is still worth printing.
		return out, err
	}
	for name, exp := range fresh {
		out[name] = exp
		cache[cacheKey(name, lang)] = exp
	}
	saveExplanationCache(cache)
	return out, nil
}

// requestExplanations sends one request covering every dish, preferring the
// `claude` CLI so that a Claude subscription is enough to use -explain.
func requestExplanations(ctx context.Context, dishes []string, lang string) (map[string]string, error) {
	system := explainSystemDA
	if lang == "en" {
		system = explainSystemEN
	}

	var list strings.Builder
	for i, name := range dishes {
		fmt.Fprintf(&list, "%d. %s\n", i, name)
	}

	var (
		answer string
		err    error
	)
	if claudeBin, lookErr := exec.LookPath("claude"); lookErr == nil {
		answer, err = askClaudeCLI(ctx, claudeBin, system, list.String())
	} else {
		answer, err = askClaudeAPI(ctx, system, list.String())
	}
	if err != nil {
		return nil, err
	}
	return parseExplanations(answer, dishes)
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
