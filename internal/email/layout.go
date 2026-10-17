package email

import (
	"bytes"
	"html/template"
	"strconv"
	"strings"
)

// DefaultHeaderColor is Talyvor's Obsidian, the header of a workspace that set no colour.
const DefaultHeaderColor = "#060A12"

// Brand is how one workspace's mail looks (B39.20). Name is empty for an unbranded workspace, which keeps the
// configured From display name.
type Brand struct {
	Name, Color, LogoURL string
}

// brandFor turns a workspace_email_branding row into a Brand: a workspace that set anything is branded with
// its from_name, and anything it left unset falls back to Talyvor's default.
func (s *Sink) brandFor(fromName, color, logoURL string) Brand {
	if fromName == "" && color == "" && logoURL == "" {
		return Brand{Color: DefaultHeaderColor, LogoURL: s.defaultLogo}
	}
	if color == "" {
		color = DefaultHeaderColor
	}
	return Brand{Name: fromName, Color: color, LogoURL: logoURL}
}

var layout = template.Must(template.New("mail").Parse(`<!doctype html>
<html><body style="margin:0;padding:0;background:#F4F7FB">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="background:#F4F7FB"><tr><td align="center" style="padding:24px 12px">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="max-width:600px;background:#FFFFFF;border-radius:6px;overflow:hidden">
<tr><td style="background:{{.Color}};padding:20px 24px">{{if .LogoURL}}<img src="{{.LogoURL}}" alt="{{.Alt}}" height="32" style="display:block;height:32px;max-width:100%;border:0">{{else}}<span style="font-family:'Space Grotesk',Arial,sans-serif;font-size:20px;font-weight:600;color:{{.Ink}}">{{.Name}}</span>{{end}}</td></tr>
<tr><td style="padding:24px;font-family:'Space Grotesk',Arial,sans-serif;font-size:15px;line-height:1.5;color:#060A12;white-space:pre-wrap">{{.Body}}</td></tr>
</table></td></tr></table></body></html>`))

// render is the HTML part of a mail: the brand's colour and logo (or name) in the header, the text body below.
func render(b Brand, body string) string {
	alt := b.Name
	if alt == "" {
		alt = "Talyvor"
	}
	var out bytes.Buffer
	_ = layout.Execute(&out, map[string]string{
		"Color": b.Color, "LogoURL": b.LogoURL, "Name": b.Name, "Alt": alt, "Ink": ink(b.Color), "Body": body,
	})
	return out.String()
}

// ink is the header text colour that reads on bg: Obsidian on a light colour, white on a dark one.
func ink(bg string) string {
	v, err := strconv.ParseUint(strings.TrimPrefix(bg, "#"), 16, 32)
	if err != nil {
		return "#FFFFFF"
	}
	r, g, b := float64(v>>16&0xff), float64(v>>8&0xff), float64(v&0xff)
	if 0.299*r+0.587*g+0.114*b > 150 {
		return "#060A12"
	}
	return "#FFFFFF"
}
