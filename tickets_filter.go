package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const trackerURL = "https://la28-kappa.vercel.app/"

type trackerCategory struct {
	Name         string  `json:"name"`
	PriceRange   string  `json:"priceRange"`
	Availability string  `json:"availability"`
	Price        float64 `json:"price,omitempty"`
}

type trackerEvent struct {
	Code                string            `json:"code"`
	Name                string            `json:"name"`
	Subtitle            string            `json:"subtitle"`
	DateTime            string            `json:"dateTime"`
	Venue               string            `json:"venue"`
	Categories          []trackerCategory `json:"categories"`
	OverallAvailability string            `json:"overallAvailability"`
	URL                 string            `json:"url"`
	Error               any               `json:"error"`
	ScrapedAt           string            `json:"scrapedAt"`
	LowestMatch         float64           `json:"lowestMatch,omitempty"`
}

type filteredTicket struct {
	Code        string            `json:"code"`
	Name        string            `json:"name"`
	Subtitle    string            `json:"subtitle,omitempty"`
	DateTime    string            `json:"dateTime"`
	Venue       string            `json:"venue"`
	Categories  []trackerCategory `json:"categories"`
	URL         string            `json:"url"`
	LowestPrice float64           `json:"lowestPrice"`
	MaxPrice    float64           `json:"maxPrice"`
	CatFilter   string            `json:"catFilter,omitempty"`
	MatchBy     string            `json:"matchBy"`
}

type categoryRule struct {
	Code    string
	Raw     string
	Letters map[rune]bool
}

func runTicketFilter(maxPrice float64, conditionsPath, pageURL string) ([]filteredTicket, error) {
	rules, err := loadCategoryRules(conditionsPath)
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", conditionsPath, err)
	}
	fmt.Printf("[filter] loaded %d event rules from %s\n", len(rules), conditionsPath)

	if maxPrice < 0 {
		maxPrice, err = promptPrice()
		if err != nil {
			return nil, err
		}
	}

	if pageURL == "" {
		pageURL = trackerURL
	}

	fmt.Printf("[filter] fetching tickets (conditions OR remaining <= $%.2f)...\n", maxPrice)
	events, err := fetchTrackerEvents(pageURL)
	if err != nil {
		return nil, err
	}

	for i := range events {
		events[i].Subtitle = cleanupText(events[i].Subtitle)
		events[i].DateTime = cleanupText(events[i].DateTime)
		for j := range events[i].Categories {
			events[i].Categories[j].PriceRange = cleanupPrice(events[i].Categories[j].PriceRange)
		}
	}

	byCond, byPrice, missing := filterConditionsOrPrice(events, rules, maxPrice)
	filtered := make([]filteredTicket, 0, len(byCond)+len(byPrice))
	catCount := 0
	appendMatch := func(e trackerEvent, matchBy, catFilter string) {
		catCount += len(e.Categories)
		filtered = append(filtered, filteredTicket{
			Code:        e.Code,
			Name:        e.Name,
			Subtitle:    e.Subtitle,
			DateTime:    e.DateTime,
			Venue:       e.Venue,
			Categories:  e.Categories,
			URL:         rewriteAffiliate(e.URL, shopAffiliate),
			LowestPrice: e.LowestMatch,
			MaxPrice:    maxPrice,
			CatFilter:   catFilter,
			MatchBy:     matchBy,
		})
	}
	for _, e := range byCond {
		appendMatch(e, "conditions", rules[strings.ToUpper(e.Code)].Raw)
	}
	for _, e := range byPrice {
		appendMatch(e, "price", "")
	}

	fmt.Printf("[filter] %d available events (%d conditions, %d price), %d categories\n",
		len(filtered), len(byCond), len(byPrice), catCount)
	if len(missing) > 0 {
		fmt.Printf("[filter] conditions with no available match: %d (%s)\n",
			len(missing), strings.Join(missing, ", "))
	}
	return filtered, nil
}

func promptPrice() (float64, error) {
	in := bufio.NewReader(os.Stdin)
	for {
		fmt.Print("Enter max price: ")
		line, err := in.ReadString('\n')
		if err != nil && len(strings.TrimSpace(line)) == 0 {
			return 0, err
		}
		line = strings.TrimSpace(line)
		line = strings.TrimPrefix(line, "$")
		line = strings.ReplaceAll(line, ",", "")
		if line == "" {
			fmt.Println("Please enter a number (e.g. 50)")
			continue
		}
		v, err := strconv.ParseFloat(line, 64)
		if err != nil || v < 0 {
			fmt.Println("Invalid price. Try again (e.g. 50)")
			continue
		}
		return v, nil
	}
}

func loadCategoryRules(path string) (map[string]categoryRule, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	rules := make(map[string]categoryRule)
	sc := bufio.NewScanner(f)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		code, raw, letters, err := parseConditionLine(line)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNo, err)
		}
		rules[code] = categoryRule{Code: code, Raw: raw, Letters: letters}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(rules) == 0 {
		return nil, fmt.Errorf("no rules found")
	}
	return rules, nil
}

var conditionRE = regexp.MustCompile(`(?i)^([A-Z0-9]+)\s*:\s*([A-Z])(?:\s+to\s+([A-Z]))?\s*$`)

func parseConditionLine(line string) (code, raw string, letters map[rune]bool, err error) {
	m := conditionRE.FindStringSubmatch(strings.TrimSpace(line))
	if m == nil {
		return "", "", nil, fmt.Errorf("bad format %q (want SWM15 : H to G or TEN37 : Q)", line)
	}
	code = strings.ToUpper(m[1])
	a := unicode.ToUpper(rune(m[2][0]))
	letters = map[rune]bool{}
	if m[3] == "" {
		letters[a] = true
		raw = string(a)
		return code, raw, letters, nil
	}
	b := unicode.ToUpper(rune(m[3][0]))
	lo, hi := a, b
	if lo > hi {
		lo, hi = hi, lo
	}
	for r := lo; r <= hi; r++ {
		letters[r] = true
	}
	raw = fmt.Sprintf("%c to %c", a, b)
	return code, raw, letters, nil
}

func categoryLetter(name string) (rune, bool) {
	re := regexp.MustCompile(`(?i)category\s+([A-Z])\b`)
	m := re.FindStringSubmatch(strings.TrimSpace(name))
	if m == nil {
		return 0, false
	}
	return unicode.ToUpper(rune(m[1][0])), true
}

func filterConditionsOrPrice(events []trackerEvent, rules map[string]categoryRule, maxPrice float64) (byCond, byPrice []trackerEvent, missing []string) {
	seenRule := make(map[string]bool)

	for _, e := range events {
		code := strings.ToUpper(e.Code)
		if rule, ok := rules[code]; ok {
			seenRule[code] = true
			matched, lowest := matchAvailableCategories(e.Categories, func(c trackerCategory, p float64) bool {
				letter, ok := categoryLetter(c.Name)
				return ok && rule.Letters[letter]
			})
			if len(matched) == 0 {
				missing = append(missing, code)
				continue
			}
			e.Categories = matched
			e.LowestMatch = lowest
			byCond = append(byCond, e)
			continue
		}

		matched, lowest := matchAvailableCategories(e.Categories, func(c trackerCategory, p float64) bool {
			return p <= maxPrice
		})
		if len(matched) == 0 {
			continue
		}
		e.Categories = matched
		e.LowestMatch = lowest
		byPrice = append(byPrice, e)
	}

	for code := range rules {
		if !seenRule[code] {
			missing = append(missing, code)
		}
	}
	return byCond, byPrice, missing
}

func isAvailableCategory(c trackerCategory) bool {
	return strings.ToLower(strings.TrimSpace(c.Availability)) == "available"
}

func matchAvailableCategories(cats []trackerCategory, keep func(trackerCategory, float64) bool) ([]trackerCategory, float64) {
	matched := make([]trackerCategory, 0)
	lowest := 0.0
	for _, c := range cats {
		if !isAvailableCategory(c) {
			continue
		}
		p, ok := parsePrice(c.PriceRange)
		if !ok {
			continue
		}
		if !keep(c, p) {
			continue
		}
		c.Price = p
		matched = append(matched, c)
		if lowest == 0 || p < lowest {
			lowest = p
		}
	}
	return matched, lowest
}

func parsePrice(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, ",", "")
	re := regexp.MustCompile(`\$?\s*(\d+(?:\.\d+)?)`)
	m := re.FindStringSubmatch(s)
	if m == nil {
		return 0, false
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

func loadFilteredTickets(path string) ([]filteredTicket, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var list []filteredTicket
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	return list, nil
}

func fetchTrackerEvents(pageURL string) ([]trackerEvent, error) {
	client := &http.Client{Timeout: 90 * time.Second}
	req, err := http.NewRequest(http.MethodGet, pageURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", fp().UA)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tracker HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	raw, err := extractNextFlightPayload(string(body))
	if err != nil {
		return nil, err
	}
	decoded, err := decodeJSString(raw)
	if err != nil {
		return nil, fmt.Errorf("decode RSC payload: %w", err)
	}

	arrJSON, err := extractEventsArray(decoded)
	if err != nil {
		return nil, err
	}

	var events []trackerEvent
	if err := json.Unmarshal([]byte(arrJSON), &events); err != nil {
		return nil, fmt.Errorf("parse events JSON: %w", err)
	}
	if len(events) == 0 {
		return nil, fmt.Errorf("no events found in tracker page")
	}
	return events, nil
}

var nextPushRE = regexp.MustCompile(`self\.__next_f\.push\(\[1,"((?:\\.|[^"\\])*)"\]\)`)

func extractNextFlightPayload(html string) (string, error) {
	matches := nextPushRE.FindAllStringSubmatch(html, -1)
	if len(matches) == 0 {
		return "", fmt.Errorf("no Next.js flight payloads found")
	}
	var b strings.Builder
	for _, m := range matches {
		b.WriteString(m[1])
	}
	return b.String(), nil
}

func decodeJSString(s string) (string, error) {
	quoted := `"` + s + `"`
	var out string
	if err := json.Unmarshal([]byte(quoted), &out); err != nil {
		return "", err
	}
	return out, nil
}

func extractEventsArray(decoded string) (string, error) {
	marker := `"events":`
	idx := strings.Index(decoded, marker)
	if idx < 0 {
		return "", fmt.Errorf(`"events" array not found in page data`)
	}
	rest := decoded[idx+len(marker):]
	start := strings.Index(rest, "[")
	if start < 0 {
		return "", fmt.Errorf("events value is not an array")
	}
	rest = rest[start:]

	depth := 0
	inStr := false
	esc := false
	for i := 0; i < len(rest); i++ {
		c := rest[i]
		if inStr {
			if esc {
				esc = false
				continue
			}
			if c == '\\' {
				esc = true
				continue
			}
			if c == '"' {
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				return rest[:i+1], nil
			}
		}
	}
	return "", fmt.Errorf("unclosed events array")
}

func cleanupText(s string) string {
	s = strings.ReplaceAll(s, "\u00a0", " ")
	s = strings.ReplaceAll(s, "\u2026", "...")
	return strings.TrimSpace(s)
}

func cleanupPrice(s string) string {
	s = strings.TrimSpace(s)
	for strings.HasPrefix(s, "$$") {
		s = "$" + strings.TrimPrefix(s, "$$")
	}
	return s
}

func rewriteAffiliate(rawURL, affiliate string) string {
	if rawURL == "" {
		return rawURL
	}
	re := regexp.MustCompile(`([?&]affiliate=)[^&]*`)
	if re.MatchString(rawURL) {
		return re.ReplaceAllString(rawURL, "${1}"+affiliate)
	}
	sep := "?"
	if strings.Contains(rawURL, "?") {
		sep = "&"
	}
	return rawURL + sep + "affiliate=" + affiliate
}
