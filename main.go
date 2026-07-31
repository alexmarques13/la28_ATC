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
	ticketsOut := flag.String("tickets-out", "filtered_tickets.json", "Write/read filtered tickets here")
	skipFilter := flag.Bool("skip-filter", false, "Skip re-fetch; load existing filtered_tickets.json for ATC")
	maxQty := flag.Int("max-qty", defaultMaxEvenQty, "Max tickets to buy (forced even; never odd)")
	skipBasket := flag.Bool("skip-basket", false, "Skip add-to-basket after login")
	openBrowser := flag.Bool("open-browser", false, "After ATC, open Chrome with bot cookies on the cart page")
	flag.Parse()

	var filtered []filteredTicket
	if !*skipFilter {
		fmt.Println("======== step 1: filter tickets ========")
		list, err := runTicketFilter(*price, *conditions, *ticketsOut, trackerURL)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error filtering tickets: %v\n", err)
			os.Exit(1)
		}
		filtered = list
		fmt.Println()
	} else {
		list, err := loadFilteredTickets(*ticketsOut)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error loading %s: %v\n", *ticketsOut, err)
			os.Exit(1)
		}
		filtered = list
		fmt.Printf("[filter] skip-filter: loaded %d ticket(s) from %s\n", len(filtered), *ticketsOut)
	}

	doBasket := !*skipBasket && !*skipLogin
	if doBasket {
		if len(filtered) == 0 {
			fmt.Fprintln(os.Stderr, "Error: ATC requires filtered tickets — none matched. Refusing freeform event ATC.")
			os.Exit(1)
		}
		if len(atcTargetsFromFiltered(filtered)) == 0 {
			fmt.Fprintln(os.Stderr, "Error: filtered tickets have no usable categories for ATC")
			os.Exit(1)
		}
		want := evenFloor(*maxQty)
		if want < 2 {
			want = defaultMaxEvenQty
			fmt.Printf("[basket] --max-qty=%d is not a valid even size; using %d\n", *maxQty, want)
		} else if want != *maxQty {
			fmt.Printf("[basket] --max-qty=%d forced down to even %d (odd ATC forbidden)\n", *maxQty, want)
		}
		*maxQty = want
		fmt.Printf("[basket] will ATC %d filtered target(s), max even qty=%d\n",
			len(atcTargetsFromFiltered(filtered)), *maxQty)
	}

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
	keepAlive := false

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
			if doBasket {
				fmt.Println("======== step 3: add to basket (filtered only) ========")
				basket, err := client.AddFilteredToBasket(filtered, *maxQty)
				if err != nil {
					fmt.Fprintf(os.Stderr, "[%s] basket failed: %v\n", combo.Email, err)
					failCount++
					client.Close()
					continue
				}
				result.Basket = basket
			}
		} else if *skipLogin {
			queue, err := client.JoinAndWait(*maxPolls)
			if err != nil {
				fmt.Fprintf(os.Stderr, "queue failed: %v\n", err)
				failCount++
				client.Close()
				continue
			}
			result = &RunResult{Queue: queue}
		} else if doBasket {
			result, err = client.RunWithBasket(combo.Email, combo.Password, *maxPolls, filtered, *maxQty)
			if err != nil {
				fmt.Fprintf(os.Stderr, "[%s] failed: %v\n", combo.Email, err)
				failCount++
				client.Close()
				continue
			}
		} else {
			result, err = client.Run(combo.Email, combo.Password, *maxPolls)
			if err != nil {
				fmt.Fprintf(os.Stderr, "[%s] failed: %v\n", combo.Email, err)
				failCount++
				client.Close()
				continue
			}
		}
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
		}

		heldForDiscord := false
		if result.Basket != nil {
			fmt.Println("\nSuccess: added to basket")
			b, _ := json.MarshalIndent(result.Basket, "", "  ")
			fmt.Println(truncate(string(b), 2000))
			if notifyCartReserved(combo.Email, result.Basket, client) {
				heldForDiscord = true
			}
			keepAlive = true // never exit right after ATC / Discord notify
			if *openBrowser {
				cartURL := result.Basket.CartURL
				if cartURL == "" {
					cartURL = baseURL + "/shoppingCart/?affiliate=28V"
				}
				fmt.Println("\n======== step 4: open cart in browser ========")
				if err := client.OpenCartInBrowser(cartURL); err != nil {
					fmt.Fprintf(os.Stderr, "[browser] warning: %v\n", err)
				}
			}
			okCount++
		} else if result.Login != nil {
			okCount++
		} else if *skipLogin {
			okCount++
		}

		// Keep the ATC client open when Discord may still open the cart from a button click.
		if !heldForDiscord {
			client.Close()
		}
	}

	if !*skipLogin {
		fmt.Printf("\n======== done: %d ok, %d failed, %d total ========\n", okCount, failCount, len(combos))
	}
	if keepAlive {
		if discordBotTokenConfigured() {
			waitForDiscordBot()
		} else {
			waitUntilInterrupt("Open Cart button armed — click it in Discord (Ctrl+C to quit)")
		}
		closeRegisteredCartClients()
		return
	}
	if failCount > 0 {
		os.Exit(1)
	}
}
