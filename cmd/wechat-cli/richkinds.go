package main

import (
	"encoding/xml"
	"fmt"
	"strconv"
	"strings"

	"github.com/r266-tech/wx-cli/v2/internal/wcdb"
)

// Message shapes found in real accounts that the base parser left as
// "[未知类型]" or a generic app placeholder: QQ mail pushes, call bubbles,
// Channels (视频号) feeds and lives, group announcements and WeChat gifts.
// Each yields the sentence WeChat shows plus a small structured payload.

type xmlPushMail struct {
	XMLName  xml.Name `xml:"msg"`
	Subject  string   `xml:"pushmail>content>subject"`
	Sender   string   `xml:"pushmail>content>sender"`
	Digest   string   `xml:"pushmail>content>digest"`
	Date     string   `xml:"pushmail>content>date"`
	Attach   string   `xml:"pushmail>content>attach"`
	FromAddr []string `xml:"pushmail>content>fromlist>item>addr"`
	ToNames  []string `xml:"pushmail>content>tolist>item>name"`
}

type xmlVoipBubble struct {
	XMLName  xml.Name `xml:"voipmsg"`
	Msg      string   `xml:"VoIPBubbleMsg>msg"`
	RoomType string   `xml:"VoIPBubbleMsg>room_type"`
	Duration string   `xml:"VoIPBubbleMsg>duration"`
}

type xmlAppmsgRich struct {
	XMLName xml.Name `xml:"msg"`
	AppMsg  struct {
		TextAnnouncement string `xml:"textannouncement"`
		FinderFeed       struct {
			Nickname string `xml:"nickname"`
			Desc     string `xml:"desc"`
			Media    []struct {
				CoverURL string `xml:"coverUrl"`
				ThumbURL string `xml:"thumbUrl"`
				Duration string `xml:"videoPlayDuration"`
				Width    string `xml:"width"`
				Height   string `xml:"height"`
			} `xml:"mediaList>media"`
		} `xml:"finderFeed"`
		FinderLive struct {
			Nickname   string `xml:"nickname"`
			Desc       string `xml:"desc"`
			LiveStatus string `xml:"liveStatus"`
		} `xml:"finderLive"`
		Gift struct {
			Wish  string   `xml:"wishmessage"`
			Title string   `xml:"gifttitle"`
			Items []string `xml:"gifts>gift>skutitle"`
		} `xml:"ecsgift"`
	} `xml:"appmsg"`
}

// parseRichKinds returns fields to merge into message_content_parsed, or nil.
func parseRichKinds(baseKind, subtype int32, xmlStr string) map[string]any {
	switch baseKind {
	case 35:
		var m xmlPushMail
		if xml.Unmarshal([]byte(xmlStr), &m) != nil {
			return nil
		}
		mail := compactMap(map[string]any{
			"subject":   strings.TrimSpace(m.Subject),
			"from":      strings.TrimSpace(m.Sender),
			"from_addr": strings.TrimSpace(firstNonEmpty(m.FromAddr...)),
			"to":        strings.Join(trimmedNonEmpty(m.ToNames), "、"),
			"digest":    strings.TrimSpace(m.Digest),
			"date":      strings.TrimSpace(m.Date),
		})
		if n, err := strconv.Atoi(strings.TrimSpace(m.Attach)); err == nil && n > 0 {
			mail["attachments"] = n
		}
		if len(mail) == 0 {
			return nil
		}
		return map[string]any{"mail": mail}
	case 50:
		var m xmlVoipBubble
		if xml.Unmarshal([]byte(xmlStr), &m) != nil {
			return nil
		}
		call := compactMap(map[string]any{"status": strings.TrimSpace(m.Msg)})
		if len(call) == 0 {
			return nil
		}
		return map[string]any{"call": call}
	case 49:
		if subtype != 51 && subtype != 63 && subtype != 87 && subtype != 124 {
			return nil
		}
		var m xmlAppmsgRich
		if xml.Unmarshal([]byte(xmlStr), &m) != nil {
			return nil
		}
		a := m.AppMsg
		switch subtype {
		case 51:
			feed := compactMap(map[string]any{
				"author":      strings.TrimSpace(a.FinderFeed.Nickname),
				"description": strings.TrimSpace(a.FinderFeed.Desc),
			})
			if len(a.FinderFeed.Media) > 0 {
				media := a.FinderFeed.Media[0]
				if d, err := strconv.Atoi(strings.TrimSpace(media.Duration)); err == nil && d > 0 {
					feed["duration_s"] = d
				}
				if cover := strings.TrimSpace(firstNonEmpty(media.CoverURL, media.ThumbURL)); cover != "" {
					feed["cover_url"] = cover
				}
			}
			if len(feed) > 0 {
				return map[string]any{"channel_video": feed}
			}
		case 63:
			live := compactMap(map[string]any{
				"author":      strings.TrimSpace(a.FinderLive.Nickname),
				"description": strings.TrimSpace(a.FinderLive.Desc),
			})
			if len(live) > 0 {
				return map[string]any{"channel_live": live}
			}
		case 87:
			if text := strings.TrimSpace(a.TextAnnouncement); text != "" {
				return map[string]any{"announcement_text": text}
			}
		case 124:
			gift := compactMap(map[string]any{
				"title": strings.TrimSpace(a.Gift.Title),
				"wish":  strings.TrimSpace(a.Gift.Wish),
				"items": trimmedNonEmpty(a.Gift.Items),
			})
			if len(gift) > 0 {
				return map[string]any{"gift": gift}
			}
		}
	}
	return nil
}

// richSummary renders the shapes parseRichKinds understands; "" otherwise.
func richSummary(baseKind, subtype int32, parsed any) string {
	p, _ := parsed.(map[string]any)
	if p == nil {
		return ""
	}
	switch {
	case baseKind == 35:
		mail := mapAny(p["mail"])
		if len(mail) == 0 {
			return ""
		}
		out := "[邮件] " + firstNonEmpty(stringMapValue(mail, "subject"), "(无主题)")
		if from := stringMapValue(mail, "from"); from != "" {
			out += " — " + from
		}
		if digest := stringMapValue(mail, "digest"); digest != "" {
			out += "：" + truncateRunes(strings.Join(strings.Fields(digest), " "), 120)
		}
		return out
	case baseKind == 50:
		if status := stringMapValue(mapAny(p["call"]), "status"); status != "" {
			return "[通话] " + status
		}
	case baseKind == 49 && subtype == 51:
		return authoredSummary("[视频号]", mapAny(p["channel_video"]))
	case baseKind == 49 && subtype == 63:
		return authoredSummary("[视频号直播]", mapAny(p["channel_live"]))
	case baseKind == 49 && subtype == 87:
		if text := stringMapValue(p, "announcement_text"); text != "" {
			return "[群公告] " + text
		}
	case baseKind == 49 && subtype == 124:
		gift := mapAny(p["gift"])
		if len(gift) == 0 {
			return ""
		}
		out := "[微信礼物]"
		if items := stringSliceAny(gift["items"]); len(items) > 0 {
			out += " " + strings.Join(items, "、")
		} else if title := stringMapValue(gift, "title"); title != "" {
			out += " " + title
		}
		if wish := stringMapValue(gift, "wish"); wish != "" {
			out += "：" + wish
		}
		return out
	}
	return ""
}

func authoredSummary(label string, m map[string]any) string {
	if len(m) == 0 {
		return ""
	}
	author, desc := stringMapValue(m, "author"), truncateRunes(strings.Join(strings.Fields(stringMapValue(m, "description")), " "), 120)
	switch {
	case author != "" && desc != "":
		return fmt.Sprintf("%s %s：%s", label, author, desc)
	case author != "":
		return label + " " + author
	case desc != "":
		return label + " " + desc
	}
	return label
}

// agentRichPayload exposes the structured part for kinds that have one.
func agentRichPayload(out map[string]any, r wcdb.Row) {
	p, _ := r["message_content_parsed"].(map[string]any)
	if p == nil {
		return
	}
	for _, key := range []string{"mail", "call", "channel_video", "channel_live", "gift"} {
		if v := mapAny(p[key]); len(v) > 0 {
			out[key] = v
		}
	}
	if text := stringMapValue(p, "announcement_text"); text != "" {
		out["announcement"] = map[string]any{"text": text}
	}
}

func trimmedNonEmpty(values []string) []string {
	var out []string
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}
