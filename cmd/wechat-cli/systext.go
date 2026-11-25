package main

import (
	"encoding/xml"
	"regexp"
	"strings"
)

// WeChat system messages (base kind 10000) arrive as plain text, inline UI
// markup, or <sysmsg> XML. Agents need the sentence WeChat would show, not
// the XML, so render the common shapes and keep anything unknown intact.

var (
	systemTalkerPrefix = regexp.MustCompile(`^[A-Za-z0-9_.-]+@chatroom:\s*\n`)
	systemInlineMarkup = regexp.MustCompile(`</?(?:_wc_custom_link_|a|img)\b[^>]*>`)
	systemWxidToken    = regexp.MustCompile(`\$([A-Za-z0-9_-]+)\$`)
)

type sysmsgXML struct {
	Type     string       `xml:"type,attr"`
	Revoke   string       `xml:"revokemsg>content"`
	Content  string       `xml:"content"`
	Template string       `xml:"sysmsgtemplate>content_template>template"`
	Plain    string       `xml:"sysmsgtemplate>content_template>plain"`
	Links    []sysmsgLink `xml:"sysmsgtemplate>content_template>link_list>link"`
	TopOp    string       `xml:"mmchatroomtopmsg>op"`
}

type sysmsgLink struct {
	Name      string   `xml:"name,attr"`
	Type      string   `xml:"type,attr"`
	Hidden    string   `xml:"hidden,attr"`
	Title     string   `xml:"title"`
	Plain     string   `xml:"plain"`
	Nicknames []string `xml:"memberlist>member>nickname"`
	Usernames []string `xml:"memberlist>member>username"`
	Separator string   `xml:"separator"`
}

func renderSystemText(raw string) string {
	text := strings.TrimSpace(systemTalkerPrefix.ReplaceAllString(strings.TrimSpace(raw), ""))
	if text == "" {
		return ""
	}
	if strings.Contains(text, "<sysmsg") {
		if rendered := renderSysmsgXML(text); rendered != "" {
			return rendered
		}
		return text
	}
	return cleanSystemInline(text)
}

func renderSysmsgXML(text string) string {
	var msg sysmsgXML
	if err := xml.Unmarshal([]byte(text), &msg); err != nil {
		return ""
	}
	switch {
	case strings.TrimSpace(msg.Revoke) != "":
		return cleanSystemInline(msg.Revoke)
	case strings.TrimSpace(msg.Template) != "":
		out := msg.Template
		for _, link := range msg.Links {
			out = strings.ReplaceAll(out, "$"+link.Name+"$", sysmsgLinkText(link))
		}
		return cleanSystemInline(out)
	case strings.TrimSpace(msg.Plain) != "":
		return cleanSystemInline(msg.Plain)
	case msg.Type == "mmchatroomtopmsg":
		if strings.TrimSpace(msg.TopOp) == "1" {
			return "[群置顶] 置顶了一条消息"
		}
		return "[群置顶] 置顶消息有变更"
	case strings.TrimSpace(msg.Content) != "":
		return cleanSystemInline(msg.Content)
	}
	return ""
}

func sysmsgLinkText(link sysmsgLink) string {
	if link.Hidden == "1" {
		return ""
	}
	names := make([]string, 0, len(link.Nicknames))
	for i, nick := range link.Nicknames {
		nick = strings.TrimSpace(nick)
		if nick == "" && i < len(link.Usernames) {
			nick = strings.TrimSpace(link.Usernames[i])
		}
		if nick != "" {
			names = append(names, nick)
		}
	}
	if len(names) > 0 {
		sep := link.Separator
		if sep == "" {
			sep = "、"
		}
		return strings.Join(names, sep)
	}
	return strings.TrimSpace(firstNonEmpty(link.Title, link.Plain))
}

func cleanSystemInline(text string) string {
	text = systemInlineMarkup.ReplaceAllString(text, "")
	text = systemWxidToken.ReplaceAllString(text, "$1")
	return strings.Join(strings.Fields(text), " ")
}
