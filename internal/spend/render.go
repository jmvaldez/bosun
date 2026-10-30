package spend

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/jmvaldez/bosun/internal/clean"
)

// Text renders a view for a terminal. Every figure is an API-rate estimate.
func Text(w io.Writer, r Result) {
	width := len("NAME")
	for _, row := range append([]Row{r.Total}, r.Rows...) {
		if k := len(clean.Text(row.Key)); k > width {
			width = k
		}
	}
	fmt.Fprintf(w, "%-*s  %8s  %8s  %26s  %12s\n", width, "NAME", "SESSIONS", "REQUESTS", "IN / OUT / CACHE", "SPEND")
	line := func(row Row) {
		cell := "$" + strconv.FormatFloat(row.USD, 'f', 2, 64)
		if row.Est {
			cell += " est."
		}
		switch {
		case row.Redacted > 0 && row.Redacted == row.Sessions:
			cell = Dash
		case row.Redacted > 0:
			cell += " +" + Dash
		}
		toks := fmt.Sprintf("%s / %s / %s", Tok(row.Input), Tok(row.Output), Tok(row.Cache))
		fmt.Fprintf(w, "%-*s  %8d  %8d  %26s  %12s\n", width, clean.Text(row.Key), row.Sessions, row.Requests, toks, cell)
	}
	for _, row := range r.Rows {
		line(row)
	}
	fmt.Fprintln(w, strings.Repeat("-", width+66))
	line(r.Total)
	if r.Total.USD != 0 {
		fmt.Fprintf(w, "%.0f%% est.   API-rate estimate\n", 100*r.Total.EstUSD/r.Total.USD)
	} else {
		fmt.Fprintln(w, "API-rate estimate")
	}
	if n := r.Total.Redacted; n > 0 {
		fmt.Fprintf(w, "%s %s without an exact cost; live cost is off, so totals leave them out\n", Dash, Plural(n, "session"))
	}
}

// Dash is what stands for an amount live cost hides.
const Dash = "\u2014"

// RedactedNote is the short suffix after a total that leaves n sessions out:
// "+ 2 —".
func RedactedNote(n int) string { return fmt.Sprintf("+ %d %s", n, Dash) }

// Plural is "1 session" or "2 sessions".
func Plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// Tok formats a token count as 1.2k / 3.4M.
func Tok(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	}
	return strconv.FormatInt(n, 10)
}

func JSON(w io.Writer, r Result) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// CSVHeader is the column order of CSV output, the same fields as JSON.
var CSVHeader = []string{"key", "sessions", "requests", "input_tokens", "output_tokens", "cache_tokens", "usd", "est_usd", "est"}

// CSV writes the same rows as JSON, then the total.
func CSV(w io.Writer, r Result) error {
	cw := csv.NewWriter(w)
	_ = cw.Write(CSVHeader)
	for _, row := range append(append([]Row{}, r.Rows...), r.Total) {
		_ = cw.Write([]string{
			clean.Text(row.Key), strconv.Itoa(row.Sessions), strconv.Itoa(row.Requests),
			strconv.FormatInt(row.Input, 10), strconv.FormatInt(row.Output, 10), strconv.FormatInt(row.Cache, 10),
			strconv.FormatFloat(row.USD, 'f', 6, 64), strconv.FormatFloat(row.EstUSD, 'f', 6, 64), strconv.FormatBool(row.Est),
		})
	}
	cw.Flush()
	return cw.Error()
}
