package app

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/chromedp/chromedp"
)

const catalog = `<select name="xnm"><option value="2025">2025–2026</option><option value="2026" selected>2026–2027</option></select><select id="xqm"><option value="3" selected="selected">第一学期</option><option value="12">第二学期</option></select>`
const fixture = `{"xsxx":{"XNM":"2026","XQM":"3"},"kbList":[{"jxb_id":"class-1","kcmc":"高等数学","xm":"张老师","cdmc":"A101","xqj":"1","zcd":"1-4周(单)","jcs":"1-2"},{"jxb_id":"class-1","kcmc":"高等数学","xm":"张老师","cdmc":"A101","xqj":"1","zcd":"1-4周(单)","jcs":"1-2"}],"sjkList":[{"kcmc":"实习","qsjsz":"1-2周"}],"xqbzxxszList":[{"jcdm":"1","qssj":"08:00","jssj":"08:45"},{"jcdm":"2","qssj":"08:50","jssj":"09:35"}]}`

func testTable(t *testing.T) Timetable {
	t.Helper()
	tt, err := parseTimetable([]byte(fixture), Term{"2026", "3", "2026 第一学期", true})
	if err != nil {
		t.Fatal(err)
	}
	return tt
}

func TestTermCatalog(t *testing.T) {
	terms, err := parseTerms(catalog)
	if err != nil || len(terms) != 4 {
		t.Fatalf("%v %v", terms, err)
	}
	for _, selector := range []string{"", "2026-1", "2026:3"} {
		term, err := pickTerm(terms, selector)
		if err != nil || term.Year != "2026" || term.Semester != "3" {
			t.Fatalf("%q: %+v %v", selector, term, err)
		}
	}
	if _, err = pickTerm(terms, "2024-1"); err == nil {
		t.Fatal("accepted absent term")
	}
}

func TestWeeks(t *testing.T) {
	for _, tc := range []struct{ input, want string }{{"1-6周(单)", "[1 3 5]"}, {"2～8周（双），11周", "[2 4 6 8 11]"}, {"1,1,2至3周", "[1 2 3]"}} {
		got, err := parseWeeks(tc.input)
		if err != nil || fmt.Sprint(got) != tc.want {
			t.Fatalf("%s -> %v %v", tc.input, got, err)
		}
	}
	for _, s := range []string{"0-3", "1-61", "6-1", "待定", "1-3,未知"} {
		if _, err := parseWeeks(s); err == nil {
			t.Fatalf("accepted %s", s)
		}
	}
}

func TestICSExport(t *testing.T) {
	tt := testTable(t)
	if len(tt.Warnings) != 1 {
		t.Fatalf("practice warning missing: %v", tt.Warnings)
	}
	ics, n, err := makeICS(tt, "2026-09-07", time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 || strings.Count(ics, "BEGIN:VEVENT") != 2 {
		t.Fatal("did not deduplicate or expand odd weeks")
	}
	for _, want := range []string{"20260907T080000", "20260921T080000", "20260907T093500", "SUMMARY:高等数学", "TZID:Asia/Shanghai", "CLASS:PRIVATE"} {
		if !strings.Contains(ics, want) {
			t.Fatalf("missing %s", want)
		}
	}
	if strings.Contains(ics, "20260914T") {
		t.Fatal("even week emitted")
	}
	if _, _, err = makeICS(tt, "", time.Now()); err == nil {
		t.Fatal("missing date accepted")
	}
	if _, _, err = makeICS(tt, "2026-09-08", time.Now()); err == nil {
		t.Fatal("non-Monday accepted")
	}
	delete(tt.Periods, 2)
	if _, _, err = makeICS(tt, "2026-09-07", time.Now()); err == nil {
		t.Fatal("missing period accepted")
	}
}

func TestDateMapAndTermMismatch(t *testing.T) {
	tt := testTable(t)
	tt.Dates = map[string]string{"1:1": "2026-09-14", "3:1": "2026-09-28"}
	if needsMonday(tt) {
		t.Fatal("authoritative dates ignored")
	}
	ics, _, err := makeICS(tt, "", time.Now())
	if err != nil || !strings.Contains(ics, "20260914T080000") {
		t.Fatal(err)
	}
	if _, err = parseTimetable([]byte(fixture), Term{Year: "2025", Semester: "3"}); err == nil {
		t.Fatal("wrong term accepted")
	}
	if _, err = parseTimetable([]byte(`<html>login</html>`), Term{}); err == nil {
		t.Fatal("login page accepted")
	}
}

func TestICSUTF8AndEscaping(t *testing.T) {
	s := strings.Repeat("课程", 40) + ",;\\\n下一行"
	line := foldICS("SUMMARY:" + escapeICS(s))
	for _, part := range strings.Split(line, "\r\n") {
		if len(part) > 75 || !utf8.ValidString(part) {
			t.Fatal("invalid folding")
		}
	}
	unfolded := strings.ReplaceAll(line, "\r\n ", "")
	if !strings.Contains(unfolded, `\,\;\\\n`) {
		t.Fatal("text escaping failed")
	}
}

func TestCLIAndOutput(t *testing.T) {
	t.Setenv("NBT_USER", "")
	t.Setenv("NBT_PASSWORD", "")
	o, err := parseOptions([]string{"123", "a'\"\\$password", "--site", "direct", "--term=2026-1", "--week-one", "2026-09-07"}, io.Discard)
	if err != nil || o.User != "123" || o.Site != "direct" || o.Password != "a'\"\\$password" {
		t.Fatalf("%+v %v", o, err)
	}
	for _, site := range []string{"vpn", "direct", "https://jwxt-443.webvpn.nbt.edu.cn/", "https://jwxt.nbt.edu.cn"} {
		if _, err := siteURL(site); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := siteURL("https://example.com"); err == nil {
		t.Fatal("unknown host accepted")
	}
	path := filepath.Join(t.TempDir(), "table.ics")
	if err = writeOutput(path, "first", false); err != nil {
		t.Fatal(err)
	}
	if err = writeOutput(path, "second", false); err == nil {
		t.Fatal("overwrote output")
	}
	content, _ := os.ReadFile(path)
	if string(content) != "first" {
		t.Fatal("existing output changed")
	}
	if err = writeOutput(path, "second", true); err != nil {
		t.Fatal(err)
	}
}

// Opt-in real Chrome integration, using only a local mock JWXT server.
// It never submits credentials to the school.
func TestBrowserIntegration(t *testing.T) {
	if os.Getenv("NBT_BROWSER_TEST") != "1" {
		t.Skip("set NBT_BROWSER_TEST=1 to run Chrome integration")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/jwglxt/kbcx/xskbcx_cxXskbcxIndex.html", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		http.SetCookie(w, &http.Cookie{Name: "session", Value: "test", Path: "/"})
		fmt.Fprint(w, catalog)
	})
	mux.HandleFunc("/jwglxt/kbcx/xskbcx_cxXsgrkb.html", func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("session")
		if err != nil || c.Value != "test" {
			http.Error(w, "no session", 401)
			return
		}
		if r.Method != "POST" || r.FormValue("xnm") != "2026" || r.FormValue("xqm") != "3" || r.Header.Get("X-Requested-With") != "XMLHttpRequest" {
			http.Error(w, "bad request", 400)
			return
		}
		fmt.Fprint(w, fixture)
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	b, cleanup, err := startBrowser(ctx, server.URL, "", true)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	page, err := b.login("", "", 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	terms, err := parseTerms(page)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := pickTerm(terms, "")
	if err != nil {
		t.Fatal(err)
	}
	data, err := b.fetch("/jwglxt/kbcx/xskbcx_cxXsgrkb.html", url.Values{"xnm": {selected.Year}, "xqm": {selected.Semester}})
	if err != nil {
		t.Fatal(err)
	}
	tt, err := parseTimetable(data, selected)
	if err != nil {
		t.Fatal(err)
	}
	_, count, err := makeICS(tt, "2026-09-07", time.Now())
	if err != nil || count != 2 {
		t.Fatalf("events=%d error=%v", count, err)
	}
	var status string
	if err = chromedp.Run(b.ctx, chromedp.Evaluate(loginScript("test", "secret"), &status)); err != nil || status != "untrusted" {
		t.Fatalf("host guard: %s %v", status, err)
	}
}
