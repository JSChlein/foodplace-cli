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
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

const baseURL = "https://salling.thefoodplace.dk/banner/weekmenu/%d"

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

type menuEntry struct {
	Date        string `json:"date"`
	StationName struct {
		station
		ParentID *station `json:"parent_id"`
	} `json:"station_name"`
	RecipeID struct {
		MenuInfo  string `json:"menu_info"`   // Danish
		MenuInfo1 string `json:"menu_info_1"` // English
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

type dish struct {
	sequence int
	name     string
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
			byDay[e.Date][cat] = dish{sequence: e.StationName.Sequence, name: name}
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

func main() {
	location := flag.Int("location", 1, "location/banner id")
	lang := flag.String("lang", "da", "menu language: da or en")
	week := flag.Int("week", 0, "ISO week number to show (0 = all weeks returned)")
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

	for _, wk := range weekOrder {
		if *week != 0 && wk != *week {
			continue
		}
		if *lang == "da" {
			fmt.Printf("\n=== Uge %d ===\n", wk)
		} else {
			fmt.Printf("\n=== Week %d ===\n", wk)
		}
		for _, d := range daysByWeek[wk] {
			fmt.Printf("\n%s (%s)\n", weekdayName(dateTime[d], *lang), d)
			for _, cat := range categoryOrder {
				if dsh, ok := byDay[d][cat]; ok {
					fmt.Printf("  %-12s %s\n", cat+":", dsh.name)
				} else {
					fmt.Printf("  %-12s -\n", cat+":")
				}
			}
		}
	}
	fmt.Println()
}
