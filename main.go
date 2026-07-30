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

	target := flag.String("target", defaultTarget, "Protected page URL (default: mycustomerdata)")
	hyperKey := flag.String("hyper-key", os.Getenv("HYPER_API_KEY"), "Hyper Solutions API key (or set HYPER_API_KEY)")
	email := flag.String("email", os.Getenv("LA28_EMAIL"), "LA28 ID email (or set LA28_EMAIL)")
	password := flag.String("password", os.Getenv("LA28_PASSWORD"), "LA28 ID password (or set LA28_PASSWORD)")
	proxy := flag.String("proxy", os.Getenv("HTTP_PROXY"), "Optional HTTP/S proxy URL")
	pollInterval := flag.Float64("poll-interval", 2.0, "Seconds between queue status polls")
	maxPolls := flag.Int("max-polls", 0, "Stop after N status polls (0 = wait until redirect)")
	skipLogin := flag.Bool("skip-login", false, "Only handle queue, do not auto-login")
	loginOnly := flag.Bool("login-only", false, "Skip queue and only run SSO login")
	flag.Parse()

	if *hyperKey == "" {
		fmt.Fprintln(os.Stderr, "Error: provide --hyper-key or set HYPER_API_KEY")
		os.Exit(1)
	}

	client, err := NewClient(*hyperKey, *target, *proxy, time.Duration(*pollInterval*float64(time.Second)))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer client.Close()

	loginEmail := *email
	loginPassword := *password
	if *skipLogin {
		loginEmail, loginPassword = "", ""
	}
	if !*skipLogin && (loginEmail == "" || loginPassword == "") {
		fmt.Fprintln(os.Stderr, "Error: provide --email/--password or LA28_EMAIL/LA28_PASSWORD (or pass --skip-login)")
		os.Exit(1)
	}

	var result *RunResult
	if *loginOnly {
		login, err := client.Login(loginEmail, loginPassword)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		result = &RunResult{Login: login}
	} else {
		result, err = client.Run(loginEmail, loginPassword, *maxPolls)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
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
}
