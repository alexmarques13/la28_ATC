package main

import (
	"fmt"

	hyper "github.com/Hyper-Solutions/hyper-sdk-go/v2"
)

type postQueueConfig struct {
	doBasket bool
	filtered []filteredTicket
	maxQty   int
}

type postQueueResult struct {
	Login  *LoginResult
	Basket *BasketResult
}

func runPostQueue(
	session *Session,
	hyperSession *hyper.Session,
	hyperKey, target, ip, email, password string,
	cfg postQueueConfig,
) (*Client, *postQueueResult, error) {
	client, err := NewClientFromSession(session, hyperSession, hyperKey, target, ip)
	if err != nil {
		return nil, nil, err
	}

	fmt.Println("======== step 3: login ========")
	login, err := client.Login(email, password)
	if err != nil {
		client.Close()
		return nil, nil, fmt.Errorf("login failed: %w", err)
	}
	result := &postQueueResult{Login: login}
	fmt.Println("\nSuccess: logged in")

	if !cfg.doBasket {
		return client, result, nil
	}

	fmt.Println("======== step 4: add to basket (filtered only) ========")
	basket, err := client.AddFilteredToBasket(cfg.filtered, cfg.maxQty)
	if err != nil {
		client.Close()
		return nil, nil, fmt.Errorf("basket failed: %w", err)
	}
	result.Basket = basket
	fmt.Println("\nSuccess: added to basket")
	return client, result, nil
}

func notifyPostQueueSuccess(email string, result *postQueueResult, client *Client) bool {
	if result == nil || result.Basket == nil || client == nil {
		return false
	}
	return notifyCartReserved(email, result.Basket, client)
}

func printPostQueueResult(result *postQueueResult) {
	if result == nil {
		return
	}
	if result.Login != nil {
		fmt.Printf("[login] UID=%s logged_in=%v\n", result.Login.UID, result.Login.LoggedIn)
	}
	if result.Basket != nil {
		fmt.Printf("[basket] qty=%d category=%s tickets=%d\n",
			result.Basket.Quantity, result.Basket.Category, result.Basket.Tickets)
	}
}

func waitAfterDiscord(keepAlive bool) {
	if !keepAlive {
		return
	}
	if discordBotTokenConfigured() {
		waitForDiscordBot()
	} else {
		waitUntilInterrupt("Open Cart button armed — click it in Discord (Ctrl+C to quit)")
	}
	closeRegisteredCartClients()
}

func loadPostQueueCombos(path string) ([]Combo, error) {
	if path == "" {
		path = "combos.txt"
	}
	return loadCombos(path)
}
