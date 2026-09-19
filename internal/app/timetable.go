// JWXT field mapping and week-expression semantics adapted from nbtca/nbtcal.
// See LICENSE.upstream and THIRD_PARTY.md.
package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"
)

type Term struct {
	Year     string
	Semester string
	Label    string
	Current  bool
}
type Period struct {
	Start string `json:"start"`
	End   string `json:"end"`
}
type Meeting struct {
	ID, Name, Teacher, Location string
	Weekday, Start, End         int
	Weeks                       []int
}
type Timetable struct {
	Term     Term
	Meetings []Meeting
	Periods  map[int]Period
	Dates    map[string]string
	Warnings []string
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}
func hasAttr(n *html.Node, key string) bool {
	for _, a := range n.Attr {
		if a.Key == key {
			return true
		}
	}
	return false
}
func nodeText(n *html.Node) string {
	if n.Type == html.TextNode {
		return n.Data
	}
	var s strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		s.WriteString(nodeText(c))
	}
	return s.String()
}
func walk(n *html.Node, visit func(*html.Node)) {
	visit(n)
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		walk(c, visit)
	}
}

func parseTerms(page string) ([]Term, error) {
	doc, err := html.Parse(strings.NewReader(page))
	if err != nil {
		return nil, err
	}
	type option struct {
		value, label string
		selected     bool
	}
	selects := map[string][]option{}
	walk(doc, func(n *html.Node) {
		if n.Type != html.ElementNode || n.Data != "select" {
			return
		}
		name := attr(n, "name")
		if name == "" {
			name = attr(n, "id")
		}
		if name != "xnm" && name != "xqm" {
			return
		}
		walk(n, func(o *html.Node) {
			if o.Type == html.ElementNode && o.Data == "option" && attr(o, "value") != "" {
				selects[name] = append(selects[name], option{attr(o, "value"), strings.TrimSpace(nodeText(o)), hasAttr(o, "selected")})
			}
		})
	})
	var terms []Term
	for _, y := range selects["xnm"] {
		for _, s := range selects["xqm"] {
			terms = append(terms, Term{y.value, s.value, y.label + " " + s.label, y.selected && s.selected})
		}
	}
	if len(terms) == 0 {
		return nil, errors.New("课表页面没有可用学期，可能尚未登录或教务系统页面已变更")
	}
	return terms, nil
}

func pickTerm(terms []Term, selector string) (Term, error) {
	for _, t := range terms {
		if selector == "" && t.Current {
			return t, nil
		}
		if selector != "" && selector == t.Year+":"+t.Semester {
			return t, nil
		}
		alias := map[string]string{"3": "1", "12": "2", "16": "3"}[t.Semester]
		if alias != "" && selector == t.Year+"-"+alias {
			return t, nil
		}
	}
	return Term{}, errors.New("未找到所选学期；请用 --list-terms 查看并通过 --term 指定")
}

type record map[string]any

func str(r record, keys ...string) string {
	for _, k := range keys {
		for _, key := range []string{k, strings.ToUpper(k)} {
			switch v := r[key].(type) {
			case string:
				if strings.TrimSpace(v) != "" {
					return strings.TrimSpace(v)
				}
			case json.Number:
				return string(v)
			case float64:
				return strconv.FormatFloat(v, 'f', -1, 64)
			}
		}
	}
	return ""
}
func rows(v any) []any { a, _ := v.([]any); return a }

var weekRange = regexp.MustCompile(`^(\d{1,2})(?:-(\d{1,2}))?$`)
var digits = regexp.MustCompile(`\d{1,2}`)
var space = regexp.MustCompile(`\s+`)
var odd = regexp.MustCompile(`[（(]\s*单\s*[)）]|单周`)
var even = regexp.MustCompile(`[（(]\s*双\s*[)）]|双周`)

func parseWeeks(expression string) ([]int, error) {
	expression = strings.NewReplacer("，", ",", "、", ",", "；", ",", ";", ",", "~", "-", "～", "-", "—", "-", "–", "-", "至", "-").Replace(expression)
	set := map[int]bool{}
	for _, part := range strings.Split(expression, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		parity := -1
		if odd.MatchString(part) {
			parity = 1
		}
		if even.MatchString(part) {
			if parity == 1 {
				return nil, errors.New("周次单双周冲突")
			}
			parity = 0
		}
		part = odd.ReplaceAllString(part, "")
		part = even.ReplaceAllString(part, "")
		part = space.ReplaceAllString(strings.NewReplacer("第", "", "周", "").Replace(part), "")
		m := weekRange.FindStringSubmatch(part)
		if m == nil {
			return nil, fmt.Errorf("无法识别周次 %q", expression)
		}
		start, _ := strconv.Atoi(m[1])
		end := start
		if m[2] != "" {
			end, _ = strconv.Atoi(m[2])
		}
		if start < 1 || end < start || end > 60 {
			return nil, errors.New("周次超出 1–60 范围")
		}
		for w := start; w <= end; w++ {
			if parity < 0 || w%2 == parity {
				set[w] = true
			}
		}
	}
	var weeks []int
	for w := range set {
		weeks = append(weeks, w)
	}
	sort.Ints(weeks)
	if len(weeks) == 0 {
		return nil, errors.New("周次为空")
	}
	return weeks, nil
}

func clockTime(s string) (string, error) {
	for _, layout := range []string{"15:04", "15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Format("15:04"), nil
		}
	}
	return "", fmt.Errorf("无效时间 %q", s)
}
func parsePeriods(payload any) map[int]Period {
	list := rows(payload)
	if r, ok := payload.(map[string]any); ok {
		list = rows(r["data"])
		if list == nil {
			list = rows(r["jcList"])
		}
	}
	result := map[int]Period{}
	for _, v := range list {
		r, ok := v.(map[string]any)
		if !ok {
			continue
		}
		n, _ := strconv.Atoi(digits.FindString(str(r, "jcdm", "jc", "jcmc")))
		start, e1 := clockTime(str(r, "qssj"))
		end, e2 := clockTime(str(r, "jssj"))
		if n > 0 && n <= 99 && e1 == nil && e2 == nil && start < end {
			result[n] = Period{start, end}
		}
	}
	return result
}
func decodeJSON(data []byte) (any, error) {
	var v any
	d := json.NewDecoder(strings.NewReader(string(data)))
	d.UseNumber()
	if err := d.Decode(&v); err != nil {
		return nil, errors.New("教务系统未返回有效 JSON，可能登录已失效")
	}
	return v, nil
}

func parseTimetable(data []byte, term Term) (Timetable, error) {
	t := Timetable{Term: term, Periods: map[int]Period{}, Dates: map[string]string{}}
	payload, err := decodeJSON(data)
	if err != nil {
		return t, err
	}
	r, ok := payload.(map[string]any)
	if !ok {
		return t, errors.New("课表返回格式错误")
	}
	if rows(r["kbList"]) == nil && rows(r["sjkList"]) == nil {
		return t, errors.New("响应缺少课程列表")
	}
	xs, _ := r["xsxx"].(map[string]any)
	if str(xs, "xnm") != term.Year || str(xs, "xqm") != term.Semester {
		return t, errors.New("教务系统返回的学期与请求不符，停止导出")
	}
	for _, key := range []string{"kbList", "sjkList"} {
		for i, v := range rows(r[key]) {
			r, ok := v.(map[string]any)
			if !ok {
				t.Warnings = append(t.Warnings, fmt.Sprintf("%s 第 %d 行格式错误，未导出", key, i+1))
				continue
			}
			weeks, e := parseWeeks(str(r, "zcd", "qsjsz"))
			weekday, _ := strconv.Atoi(str(r, "xqj"))
			rangeText := str(r, "jcs", "jc")
			ns := digits.FindAllString(rangeText, -1)
			start, end := 0, 0
			if len(ns) > 0 {
				start, _ = strconv.Atoi(ns[0])
				end, _ = strconv.Atoi(ns[len(ns)-1])
			}
			name := str(r, "kcmc")
			if name == "" || weekday < 1 || weekday > 7 || e != nil || start < 1 || end < start || end > 99 {
				t.Warnings = append(t.Warnings, fmt.Sprintf("%s 第 %d 行（%s）缺少可确定的周次、星期或节次，未导出；请在教务页面核对实习/实践课程", key, i+1, name))
				continue
			}
			t.Meetings = append(t.Meetings, Meeting{str(r, "jxb_id", "jxbid"), name, str(r, "xm", "jsxm"), str(r, "cdmc"), weekday, start, end, weeks})
		}
	}
	t.Periods = parsePeriods(r["xqbzxxszList"])
	conflicts := map[string]bool{}
	for _, v := range rows(r["rqazcList"]) {
		r, ok := v.(map[string]any)
		if !ok {
			continue
		}
		w, _ := strconv.Atoi(str(r, "zc"))
		d, _ := strconv.Atoi(str(r, "xqj"))
		date := str(r, "rq")
		parsed, e := time.Parse("2006-01-02", date)
		if e != nil || w < 1 || w > 60 || d < 1 || d > 7 || (int(parsed.Weekday())+6)%7+1 != d {
			continue
		}
		key := fmt.Sprintf("%d:%d", w, d)
		if conflicts[key] {
			continue
		}
		if old, ok := t.Dates[key]; ok && old != date {
			delete(t.Dates, key)
			conflicts[key] = true
			continue
		}
		t.Dates[key] = date
	}
	return t, nil
}
