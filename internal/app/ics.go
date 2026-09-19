package app

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

func mondayDate(value string) (time.Time, error) {
	t, err := time.Parse("2006-01-02", value)
	if err != nil || t.Weekday() != time.Monday {
		return time.Time{}, errors.New("第一周周一必须是 YYYY-MM-DD 格式的周一日期")
	}
	return t, nil
}

func needsMonday(t Timetable) bool {
	for _, m := range t.Meetings {
		for _, w := range m.Weeks {
			if t.Dates[fmt.Sprintf("%d:%d", w, m.Weekday)] == "" {
				return true
			}
		}
	}
	return false
}

func escapeICS(s string) string {
	s = strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(s)
	return strings.NewReplacer("\\", "\\\\", "\n", "\\n", ";", "\\;", ",", "\\,").Replace(s)
}
func foldICS(s string) string {
	var out strings.Builder
	count := 0
	for _, r := range s {
		part := string(r)
		if count+len(part) > 75 {
			out.WriteString("\r\n ")
			count = 1
		}
		out.WriteString(part)
		count += len(part)
	}
	return out.String()
}

func makeICS(t Timetable, monday string, now time.Time) (string, int, error) {
	var first time.Time
	if monday != "" {
		var err error
		first, err = mondayDate(monday)
		if err != nil {
			return "", 0, err
		}
	}
	type event struct {
		date, start, end, uid string
		m                     Meeting
	}
	var events []event
	seen := map[string]bool{}
	for _, m := range t.Meetings {
		p, ok := t.Periods[m.Start]
		q, ok2 := t.Periods[m.End]
		if !ok || !ok2 {
			return "", 0, fmt.Errorf("缺少第 %d 或 %d 节上课时间；请提供 --periods periods.json", m.Start, m.End)
		}
		start, e1 := clockTime(p.Start)
		end, e2 := clockTime(q.End)
		if e1 != nil || e2 != nil || start >= end {
			return "", 0, errors.New("课程起止时间无效，请检查 --periods")
		}
		for _, w := range m.Weeks {
			date := t.Dates[fmt.Sprintf("%d:%d", w, m.Weekday)]
			if date == "" {
				if first.IsZero() {
					return "", 0, errors.New("缺少第一周周一日期，请使用 --week-one YYYY-MM-DD")
				}
				date = first.AddDate(0, 0, (w-1)*7+m.Weekday-1).Format("2006-01-02")
			}
			// Deterministic identity, with no student ID or credentials in the ICS.
			identity := fmt.Sprintf("%s\x1f%s\x1f%s\x1f%s\x1f%s\x1f%s\x1f%d\x1f%d\x1f%d\x1f%d", t.Term.Year, t.Term.Semester, m.ID, m.Name, m.Teacher, m.Location, m.Weekday, m.Start, m.End, w)
			if seen[identity] {
				continue
			}
			seen[identity] = true
			h := sha256.Sum256([]byte(identity))
			uid := fmt.Sprintf("nbt-%x@nbt-export.local", h[:16])
			events = append(events, event{strings.ReplaceAll(date, "-", ""), strings.ReplaceAll(start, ":", "") + "00", strings.ReplaceAll(end, ":", "") + "00", uid, m})
		}
	}
	sort.Slice(events, func(i, j int) bool { a, b := events[i], events[j]; return a.date+a.start+a.uid < b.date+b.start+b.uid })
	lines := []string{"BEGIN:VCALENDAR", "VERSION:2.0", "PRODID:-//NBT Export//Timetable//ZH-CN", "CALSCALE:GREGORIAN", "METHOD:PUBLISH", "X-WR-CALNAME:" + escapeICS("个人课表 "+t.Term.Label), "X-WR-TIMEZONE:Asia/Shanghai", "BEGIN:VTIMEZONE", "TZID:Asia/Shanghai", "BEGIN:STANDARD", "TZOFFSETFROM:+0800", "TZOFFSETTO:+0800", "TZNAME:CST", "DTSTART:19700101T000000", "END:STANDARD", "END:VTIMEZONE"}
	stamp := now.UTC().Format("20060102T150405Z")
	for _, e := range events {
		lines = append(lines, "BEGIN:VEVENT", "UID:"+e.uid, "DTSTAMP:"+stamp, "DTSTART;TZID=Asia/Shanghai:"+e.date+"T"+e.start, "DTEND;TZID=Asia/Shanghai:"+e.date+"T"+e.end, "SUMMARY:"+escapeICS(e.m.Name), "CLASS:PRIVATE")
		if e.m.Location != "" {
			lines = append(lines, "LOCATION:"+escapeICS(e.m.Location))
		}
		if e.m.Teacher != "" {
			lines = append(lines, "DESCRIPTION:"+escapeICS("教师："+e.m.Teacher))
		}
		lines = append(lines, "SEQUENCE:0", "STATUS:CONFIRMED", "END:VEVENT")
	}
	lines = append(lines, "END:VCALENDAR")
	for i, line := range lines {
		lines[i] = foldICS(line)
	}
	return strings.Join(lines, "\r\n") + "\r\n", len(events), nil
}
