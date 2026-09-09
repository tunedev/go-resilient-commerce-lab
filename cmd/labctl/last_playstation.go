package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"
)

const (
	lastPlaystationSKU    = "playstation-5"
	lastPlaystationBuyers = 16
)

func init() {
	register(scenario{
		name:        "last-playstation",
		description: "sixteen buyers racing for one console produce exactly one winner",
		service:     "inventory",
		run:         runLastPlaystation,
	})
}

type reservationCreated struct {
	ReservationID string `json:"reservation_id"`
}

type errorEnvelope struct {
	Error struct {
		Code string `json:"code"`
	} `json:"error"`
}

func runLastPlaystation(ctx context.Context, env environment) error {
	client := newInventoryClient(env.inventoryBaseURL)
	if err := client.waitReady(ctx, 60*time.Second); err != nil {
		return err
	}

	stocked, err := client.setStock(ctx, lastPlaystationSKU, 1)
	if err != nil {
		return err
	}
	if stocked.Status != http.StatusOK {
		return fmt.Errorf("PUT /inventory/items/%s returned %d: %s", lastPlaystationSKU, stocked.Status, stocked.Body)
	}

	if err := warmService(ctx, client, lastPlaystationBuyers); err != nil {
		return fmt.Errorf("warm inventory connection pool: %w", err)
	}

	runID := time.Now().UnixNano()
	responses := make([]response, lastPlaystationBuyers)

	var group errgroup.Group
	for i := range lastPlaystationBuyers {
		group.Go(func() error {
			orderID := fmt.Sprintf("order-last-playstation-%d-%d", runID, i)
			idempotencyKey := fmt.Sprintf("last-playstation-%d-%d", runID, i)
			resp, err := client.reserve(ctx, orderID, lastPlaystationSKU, 1, idempotencyKey)
			if err != nil {
				return err
			}
			responses[i] = resp
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return err
	}

	return assertOneWinner(responses)
}

// warmService opens server-side database connections up to min(n, the pool's
// MaxConns), so the race that follows is decided by the stock guard and not by
// connection setup: a cold inventory pool serializes buyers behind TCP,
// Postgres startup and auth on the request path, spreading them far enough
// apart that the race window a broken guard would expose is never entered.
func warmService(ctx context.Context, client *inventoryClient, n int) error {
	group, groupCtx := errgroup.WithContext(ctx)
	for range n {
		group.Go(func() error { return client.ping(groupCtx) })
	}
	return group.Wait()
}

// assertOneWinner checks that exactly one buyer's reservation succeeded and
// that every loser was rejected for insufficient stock, not some other
// conflict. It tallies every response status and prints the histogram before
// returning a status error, so a failing run shows the shape of the break
// rather than just the first response it happened to see.
func assertOneWinner(responses []response) error {
	tally := map[int]int{}
	var winnerID string
	winners := 0
	losers := 0
	var failure error

	for _, resp := range responses {
		tally[resp.Status]++
		switch resp.Status {
		case http.StatusCreated:
			var created reservationCreated
			if err := json.Unmarshal(resp.Body, &created); err != nil {
				return fmt.Errorf("parse winning reservation: %w", err)
			}
			winnerID = created.ReservationID
			winners++
		case http.StatusConflict:
			var envelope errorEnvelope
			if err := json.Unmarshal(resp.Body, &envelope); err != nil {
				return fmt.Errorf("parse loser response: %w", err)
			}
			if envelope.Error.Code != "insufficient_stock" && failure == nil {
				failure = fmt.Errorf("loser carried code %q, want insufficient_stock", envelope.Error.Code)
			}
			losers++
		default:
			if failure == nil {
				failure = fmt.Errorf("buyer got unexpected status %d: %s", resp.Status, resp.Body)
			}
		}
	}

	fmt.Printf("%d buyers: %s\n", len(responses), histogram(tally))

	if failure != nil {
		return failure
	}
	if winners != 1 {
		return fmt.Errorf("got %d winners, want exactly 1", winners)
	}
	if losers != lastPlaystationBuyers-1 {
		return fmt.Errorf("got %d losers, want %d", losers, lastPlaystationBuyers-1)
	}

	fmt.Printf("winner: reservation %s\n", winnerID)
	fmt.Printf("losers: %d buyers got %d insufficient_stock\n", losers, http.StatusConflict)
	return nil
}

// histogram renders a status tally as "1x201, 6x409, 9x500", ascending by
// status code.
func histogram(tally map[int]int) string {
	statuses := make([]int, 0, len(tally))
	for status := range tally {
		statuses = append(statuses, status)
	}
	sort.Ints(statuses)

	parts := make([]string, len(statuses))
	for i, status := range statuses {
		parts[i] = fmt.Sprintf("%dx%d", tally[status], status)
	}
	return strings.Join(parts, ", ")
}
