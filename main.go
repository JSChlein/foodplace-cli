// Command foodplace reads the weekly menu from The Food Place and prints it per day.
//
// The page is a Remix app that embeds all its data in a `window.__remixContext`
// JSON blob. We pull that blob, walk to the menu entries, and group dishes by day.
//
// Only three categories are printed — Go Green, Deli and Comfort Food — and for
// each category only the first dish of the day is shown.
//
// Usage:
//
//	go run . [-location N] [-lang da|en] [-week N]
//	go run . upgrade            # replace the binary with the latest release
package main

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
)

const baseURL = "https://salling.thefoodplace.dk/banner/weekmenu/%d"

// binName is the executable/asset name; repo is the GitHub repository that
// hosts the releases the `upgrade` command pulls from.
const (
	binName = "foodplace"
	repo    = "JSChlein/foodplace-cli"
)

// The categories we care about, in display order. A dish is matched to a
// category either by its own station name or by its parent station name.
const (
	catGoGreen = "Go Green"
	catDeli    = "Deli"
	catComfort = "Comfort Food"
)

var categoryOrder = []string{catGoGreen, catDeli, catComfort}

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

var (
	weekdaysDA = [...]string{"Mandag", "Tirsdag", "Onsdag", "Torsdag", "Fredag", "Lørdag", "Søndag"}
	weekdaysEN = [...]string{"Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday", "Sunday"}

	remixRe = regexp.MustCompile(`(?s)window\.__remixContext\s*=\s*(\{.*?\});`)
)

// Subset of the __remixContext shape that we care about.
type remixContext struct {
	RouteData map[string]routeData `json:"routeData"`
}

type routeData struct {
	Data struct {
		Data []menuEntry `json:"data"`
	} `json:"data"`
}

type station struct {
	StationName string `json:"station_name"`
	Sequence    int    `json:"sequence"`
}

// allergen is one entry of a recipe's allergens_list. The site nests the
// details under an "Allergens_id" object.
type allergen struct {
	AllergensID struct {
		Name           string `json:"name"`            // Danish
		TranslatedName string `json:"translated_name"` // English
	} `json:"Allergens_id"`
}

type menuEntry struct {
	Date        string `json:"date"`
	StationName struct {
		station
		ParentID *station `json:"parent_id"`
	} `json:"station_name"`
	RecipeID struct {
		MenuInfo      string     `json:"menu_info"`      // Danish
		MenuInfo1     string     `json:"menu_info_1"`    // English
		AllergensList []allergen `json:"allergens_list"` // may be empty/absent
	} `json:"recipe_id"`
}

// category returns the display category for an entry, or "" if it belongs to a
// station we don't care about.
func (e menuEntry) category() string {
	switch strings.TrimSpace(e.StationName.StationName) {
	case "Go Green":
		return catGoGreen
	case "Comfort Food":
		return catComfort
	}
	if e.StationName.ParentID != nil &&
		strings.TrimSpace(e.StationName.ParentID.StationName) == "The Deli" {
		return catDeli
	}
	return ""
}

// allergens returns the de-duplicated allergen names for an entry in the given
// language, falling back to the other language when one is missing. Returns nil
// when the recipe carries no allergens.
func (e menuEntry) allergens(lang string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, a := range e.RecipeID.AllergensList {
		name := strings.TrimSpace(a.AllergensID.Name)
		if lang == "en" {
			if t := strings.TrimSpace(a.AllergensID.TranslatedName); t != "" {
				name = t
			}
		} else if name == "" {
			name = strings.TrimSpace(a.AllergensID.TranslatedName)
		}
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

type dish struct {
	sequence  int
	name      string
	allergens []string
}

func fetchHTML(location int) (string, error) {
	url := fmt.Sprintf(baseURL, location)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (menu-bot)")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status: %s", resp.Status)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func extractEntries(html string) ([]menuEntry, error) {
	m := remixRe.FindStringSubmatch(html)
	if m == nil {
		return nil, fmt.Errorf("could not find __remixContext in page — site layout may have changed")
	}
	var ctx remixContext
	if err := json.Unmarshal([]byte(m[1]), &ctx); err != nil {
		return nil, fmt.Errorf("parsing remix context: %w", err)
	}
	for key, rd := range ctx.RouteData {
		if strings.Contains(key, "weekmenu") {
			return rd.Data.Data, nil
		}
	}
	return nil, fmt.Errorf("no weekmenu route found in page data")
}

// parseMenu returns, per ISO date, the first dish (lowest station sequence) in
// each of the three categories we care about.
func parseMenu(entries []menuEntry, lang string) map[string]map[string]dish {
	byDay := make(map[string]map[string]dish)
	for _, e := range entries {
		cat := e.category()
		if cat == "" {
			continue
		}
		name := strings.TrimSpace(e.RecipeID.MenuInfo)
		if lang == "en" && strings.TrimSpace(e.RecipeID.MenuInfo1) != "" {
			name = strings.TrimSpace(e.RecipeID.MenuInfo1)
		}
		if name == "" {
			continue
		}
		if byDay[e.Date] == nil {
			byDay[e.Date] = make(map[string]dish)
		}
		// keep only the first entry per category (lowest sequence)
		if cur, ok := byDay[e.Date][cat]; !ok || e.StationName.Sequence < cur.sequence {
			byDay[e.Date][cat] = dish{
				sequence:  e.StationName.Sequence,
				name:      name,
				allergens: e.allergens(lang),
			}
		}
	}
	return byDay
}

func weekdayName(t time.Time, lang string) string {
	// Go's time.Weekday: Sunday=0 ... Saturday=6. Our slices are Monday-first.
	idx := (int(t.Weekday()) + 6) % 7
	if lang == "en" {
		return weekdaysEN[idx]
	}
	return weekdaysDA[idx]
}

// latestReleaseTag asks the GitHub API for the tag of the newest release.
func latestReleaseTag() (string, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", repo)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", binName)
	req.Header.Set("Accept", "application/vnd.github+json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub API returned %s", resp.Status)
	}

	var rel struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return "", fmt.Errorf("parsing release info: %w", err)
	}
	if rel.TagName == "" {
		return "", fmt.Errorf("could not determine latest release tag")
	}
	return rel.TagName, nil
}

// downloadReleaseBinary fetches the release archive for the given tag and the
// running OS/arch, and returns the raw bytes of the foodplace binary inside it.
func downloadReleaseBinary(tag string) ([]byte, error) {
	if runtime.GOOS == "windows" {
		return nil, fmt.Errorf(
			"self-upgrade isn't supported on Windows — download the .zip from https://github.com/%s/releases", repo)
	}
	asset := fmt.Sprintf("%s_%s_%s_%s", binName, tag, runtime.GOOS, runtime.GOARCH)
	url := fmt.Sprintf("https://github.com/%s/releases/download/%s/%s.tar.gz", repo, tag, asset)

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", binName)

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("downloading %s: %s", url, resp.Status)
	}

	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading gzip stream: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading archive: %w", err)
		}
		// Archives contain "<asset>/foodplace"; match on the base name.
		if filepath.Base(hdr.Name) == binName && hdr.Typeflag == tar.TypeReg {
			data, err := io.ReadAll(tr)
			if err != nil {
				return nil, fmt.Errorf("extracting binary: %w", err)
			}
			return data, nil
		}
	}
	return nil, fmt.Errorf("binary %q not found in release archive", binName)
}

// runUpgrade replaces the running binary with the latest release, in place.
func runUpgrade() error {
	fmt.Println("Checking for the latest release ...")
	tag, err := latestReleaseTag()
	if err != nil {
		return err
	}
	if tag == version {
		fmt.Printf("Already up to date (%s).\n", version)
		return nil
	}

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locating current binary: %w", err)
	}
	// Resolve symlinks so we replace the real file, not a link to it.
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}

	fmt.Printf("Updating %s -> %s ...\n", version, tag)
	data, err := downloadReleaseBinary(tag)
	if err != nil {
		return err
	}

	// Write to a temp file next to the target so the final rename is atomic
	// (os.Rename can't cross filesystems).
	dir := filepath.Dir(exe)
	tmp, err := os.CreateTemp(dir, ".foodplace-upgrade-*")
	if err != nil {
		return fmt.Errorf("cannot write to %s (try re-running with sudo): %w", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename below succeeds

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("writing new binary: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o755); err != nil {
		return fmt.Errorf("setting permissions: %w", err)
	}
	if err := os.Rename(tmpName, exe); err != nil {
		return fmt.Errorf("replacing %s (try re-running with sudo): %w", exe, err)
	}

	fmt.Printf("Upgraded to %s.\n", tag)
	return nil
}

func main() {
	// Subcommands are handled before flag parsing so they don't collide with
	// the menu flags.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "upgrade", "update":
			if err := runUpgrade(); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return
		}
	}

	location := flag.Int("location", 1, "location/banner id")
	lang := flag.String("lang", "da", "menu language: da or en")
	week := flag.Int("week", 0, "ISO week number to show (0 = all weeks returned)")
	allergy := flag.Bool("allergy", false, "also print each dish's allergens")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("foodplace %s\n", version)
		return
	}

	if *lang != "da" && *lang != "en" {
		fmt.Fprintln(os.Stderr, "Error: -lang must be 'da' or 'en'")
		os.Exit(2)
	}

	html, err := fetchHTML(*location)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	entries, err := extractEntries(html)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	byDay := parseMenu(entries, *lang)
	if len(byDay) == 0 {
		fmt.Fprintln(os.Stderr, "No menu items found.")
		os.Exit(1)
	}

	// Sort the dates, then group them by ISO week.
	dates := make([]string, 0, len(byDay))
	for d := range byDay {
		dates = append(dates, d)
	}
	sort.Strings(dates)

	weekOrder := []int{}
	daysByWeek := map[int][]string{}
	dateTime := map[string]time.Time{}
	for _, d := range dates {
		t, err := time.Parse("2006-01-02", d)
		if err != nil {
			continue
		}
		dateTime[d] = t
		_, wk := t.ISOWeek()
		if _, seen := daysByWeek[wk]; !seen {
			weekOrder = append(weekOrder, wk)
		}
		daysByWeek[wk] = append(daysByWeek[wk], d)
	}

	// Only show days that are today or in the future. ISO date strings
	// (YYYY-MM-DD) compare lexicographically, so a string compare is enough.
	today := time.Now().Format("2006-01-02")
	allergensLabel := "Allergener"
	if *lang == "en" {
		allergensLabel = "Allergens"
	}

	for _, wk := range weekOrder {
		if *week != 0 && wk != *week {
			continue
		}

		days := make([]string, 0, len(daysByWeek[wk]))
		for _, d := range daysByWeek[wk] {
			if d >= today {
				days = append(days, d)
			}
		}
		if len(days) == 0 {
			continue // whole week is in the past
		}

		if *lang == "da" {
			fmt.Printf("\n=== Uge %d ===\n", wk)
		} else {
			fmt.Printf("\n=== Week %d ===\n", wk)
		}
		for _, d := range days {
			fmt.Printf("\n%s (%s)\n", weekdayName(dateTime[d], *lang), d)
			for _, cat := range categoryOrder {
				dsh, ok := byDay[d][cat]
				if !ok {
					fmt.Printf("  %-12s -\n", cat+":")
					continue
				}
				fmt.Printf("  %-12s %s\n", cat+":", dsh.name)
				if *allergy && len(dsh.allergens) > 0 {
					fmt.Printf("  %-12s %s: %s\n", "", allergensLabel, strings.Join(dsh.allergens, ", "))
				}
			}
		}
	}
	fmt.Println()
}
