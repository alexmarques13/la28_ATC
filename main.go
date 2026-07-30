package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/joho/godotenv"
)

func main() {
	_ = godotenv.Load()

	target := flag.String("target", defaultTarget, "Protected page URL (default: affiliate=28V&forceLogin=true)")
	hyperKey := flag.String("hyper-key", os.Getenv("HYPER_API_KEY"), "Hyper Solutions API key (or set HYPER_API_KEY)")
	combosPath := flag.String("combos", "combos.txt", "Path to email:pass combo file")
	proxy := flag.String("proxy", os.Getenv("HTTP_PROXY"), "Optional HTTP/S proxy URL")
	pollInterval := flag.Float64("poll-interval", 2.0, "Seconds between queue status polls")
	maxPolls := flag.Int("max-polls", 0, "Stop after N status polls (0 = wait until redirect)")
	skipLogin := flag.Bool("skip-login", false, "Only handle queue, do not auto-login")
	loginOnly := flag.Bool("login-only", false, "Skip queue and only run SSO login")
	price := flag.Float64("price", -1, "Max ticket price for filter (prompted if omitted)")
	conditions := flag.String("conditions", "filter_conditions.txt", "Static event/category filter file")
	ticketsOut := flag.String("tickets-out", "filtered_tickets.json", "Write filtered tickets here")
	skipFilter := flag.Bool("skip-filter", false, "Skip ticket filter step")
	flag.Parse()

	// 1) Filter tickets first
	if !*skipFilter {
		fmt.Println("======== step 1: filter tickets ========")
		_, err := runTicketFilter(*price, *conditions, *ticketsOut, trackerURL)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error filtering tickets: %v\n", err)
			os.Exit(1)
		}
		fmt.Println()
	}

	// 2) Queue + auto-login
	fmt.Println("======== step 2: queue + login ========")
	if *hyperKey == "" {
		fmt.Fprintln(os.Stderr, "Error: provide --hyper-key or set HYPER_API_KEY")
		os.Exit(1)
	}

	var combos []Combo
	if !*skipLogin {
		var err error
		combos, err = loadCombos(*combosPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error loading combos: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("[combos] loaded %d account(s) from %s\n", len(combos), *combosPath)
	} else {
		combos = []Combo{{}}
	}

	okCount := 0
	failCount := 0

	for i, combo := range combos {
		if !*skipLogin {
			fmt.Printf("\n======== [%d/%d] %s ========\n", i+1, len(combos), combo.Email)
		}

		client, err := NewClient(*hyperKey, *target, *proxy, time.Duration(*pollInterval*float64(time.Second)))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			failCount++
			continue
		}

		var result *RunResult
		if *loginOnly {
			login, err := client.Login(combo.Email, combo.Password)
			if err != nil {
				fmt.Fprintf(os.Stderr, "[%s] login failed: %v\n", combo.Email, err)
				failCount++
				client.Close()
				continue
			}
			result = &RunResult{Login: login}
		} else if *skipLogin {
			queue, err := client.JoinAndWait(*maxPolls)
			if err != nil {
				fmt.Fprintf(os.Stderr, "queue failed: %v\n", err)
				failCount++
				client.Close()
				continue
			}
			result = &RunResult{Queue: queue}
		} else {
			result, err = client.Run(combo.Email, combo.Password, *maxPolls)
			if err != nil {
				fmt.Fprintf(os.Stderr, "[%s] failed: %v\n", combo.Email, err)
				failCount++
				client.Close()
				continue
			}
		}
		client.Close()

		if result.Queue != nil && result.Queue.RedirectURL != "" {
			fmt.Println("\nSuccess: passed queue")
			fmt.Printf("Queue URL: %s\n", result.Queue.RedirectURL)
		} else if result.Queue != nil {
			fmt.Println("\nWaiting in queue (poll limit reached or still waiting)")
			b, _ := json.MarshalIndent(json.RawMessage(result.Queue.Raw), "", "  ")
			fmt.Println(truncate(string(b), 2000))
		}

		if result.Login != nil {
			fmt.Println("\nSuccess: logged in")
			b, _ := json.MarshalIndent(result.Login, "", "  ")
			fmt.Println(truncate(string(b), 2000))
			okCount++
		} else if *skipLogin {
			okCount++
		}
	}

	if !*skipLogin {
		fmt.Printf("\n======== done: %d ok, %d failed, %d total ========\n", okCount, failCount, len(combos))
	}
	if failCount > 0 {
		os.Exit(1)
	}
}
