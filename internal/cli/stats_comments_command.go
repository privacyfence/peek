package cli

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// --- stats ---

func cmdStats(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: peek stats <slug>")
	}
	seg, err := slugPath(args[0])
	if err != nil {
		return err
	}
	cfg, err := LoadConfig()
	if err != nil {
		return err
	}
	c, err := newClient(cfg)
	if err != nil {
		return err
	}
	var st struct {
		Slug           string `json:"slug"`
		Name           string `json:"name"`
		TotalVisits    int    `json:"total_visits"`
		UniqueVisitors int    `json:"unique_visitors"`
		Recent         []struct {
			Name      string `json:"name"`
			IP        string `json:"ip"`
			UA        string `json:"user_agent"`
			Timestamp int64  `json:"visited_at"`
		} `json:"recent"`
		TokenCalls []struct {
			Caller     string `json:"caller"`
			Email      string `json:"email"`
			Version    int    `json:"version"`
			Calls      int64  `json:"calls"`
			LastCallAt int64  `json:"last_call_at"`
			LastStatus int    `json:"last_status"`
		} `json:"token_calls"`
	}
	if err := c.getJSON("/api/uploads/"+seg+"/stats", &st); err != nil {
		return err
	}
	fmt.Printf("slug:            %s\n", st.Slug)
	fmt.Printf("name:            %s\n", st.Name)
	fmt.Printf("total visits:    %d\n", st.TotalVisits)
	fmt.Printf("unique visitors: %d\n", st.UniqueVisitors)
	if len(st.Recent) > 0 {
		fmt.Println("\nrecent visits:")
		fmt.Printf("  %-20s  %-16s  %-20s  %s\n", "WHEN", "NAME", "IP (hashed)", "USER AGENT")
		for _, v := range st.Recent {
			t := time.Unix(v.Timestamp, 0).Format("2006-01-02 15:04")
			name := v.Name
			if name == "" {
				name = "(anonymous)"
			}
			fmt.Printf("  %-20s  %-16s  %-20s  %s\n", t, name, truncate(v.IP, 20), truncate(v.UA, 40))
		}
	}
	if len(st.TokenCalls) > 0 {
		fmt.Println("\ntoken calls:")
		fmt.Printf("  %-32s  %-7s  %-6s  %-16s  %s\n", "CALLER", "VERSION", "CALLS", "LAST CALL", "STATUS")
		for _, tc := range st.TokenCalls {
			who := tc.Email
			if who == "" {
				short := tc.Caller
				if len(short) > 8 {
					short = short[:8]
				}
				who = "caller " + short + "…"
			}
			when := time.Unix(tc.LastCallAt, 0).Format("2006-01-02 15:04")
			fmt.Printf("  %-32s  %-7d  %-6d  %-16s  %d\n", who, tc.Version, tc.Calls, when, tc.LastStatus)
		}
	}
	return nil
}

// --- comments ---

func cmdComments(args []string) error {
	const commentsUsage = "usage: peek comments <slug> [--version <n>]"
	var slug string
	version := 0
	for i := 0; i < len(args); i++ {
		if args[i] == "--version" {
			if i+1 >= len(args) {
				return fmt.Errorf("--version must be a positive integer")
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil || n < 1 {
				return fmt.Errorf("--version must be a positive integer")
			}
			version = n
			i++
			continue
		}
		if slug != "" || strings.HasPrefix(args[i], "-") {
			return errors.New(commentsUsage)
		}
		slug = args[i]
	}
	if slug == "" {
		return errors.New(commentsUsage)
	}
	seg, err := slugPath(slug)
	if err != nil {
		return err
	}
	cfg, err := LoadConfig()
	if err != nil {
		return err
	}
	c, err := newClient(cfg)
	if err != nil {
		return err
	}
	var list []struct {
		ID         int64  `json:"id"`
		Selector   string `json:"selector"`
		Text       string `json:"element_text"`
		AnchorKind string `json:"anchor_kind"`
		Author     string `json:"author"`
		Body       string `json:"body"`
		CreatedAt  int64  `json:"created_at"`
		Version    int    `json:"version"`
	}
	path := "/api/uploads/" + seg + "/comments"
	if version > 0 {
		path += "?v=" + strconv.Itoa(version)
	}
	if err := c.getJSON(path, &list); err != nil {
		return err
	}
	if len(list) == 0 {
		fmt.Println("no comments yet.")
		return nil
	}
	for i, cm := range list {
		when := time.Unix(cm.CreatedAt, 0).Format("2006-01-02 15:04")
		author := cm.Author
		if author == "" {
			author = "anonymous"
		}
		// Context: the on-page anchor a comment points at.
		var ctx string
		kind := cm.AnchorKind
		if kind == "" {
			if cm.Selector == "" {
				kind = "page"
			} else if cm.Text != "" {
				kind = "text"
			} else {
				kind = "element"
			}
		}
		switch {
		case kind == "text" && cm.Text != "":
			ctx = "“" + truncate(cm.Text, 60) + "”"
		case cm.Selector != "":
			ctx = cm.Selector
		default:
			ctx = "whole page"
		}
		if i > 0 {
			fmt.Println()
		}
		fmt.Printf("%s · v%d · %s\n", author, cm.Version, when)
		fmt.Printf("  on: %s\n", ctx)
		fmt.Printf("  %s\n", cm.Body)
	}
	return nil
}
