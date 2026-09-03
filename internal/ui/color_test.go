package ui

import "image/color"

// lightBackground stands in for a terminal reporting a light background.
type lightBackground struct{}

// RGBA implements color.Color.
func (lightBackground) RGBA() (r, g, b, a uint32) {
	return 0xffff, 0xffff, 0xffff, 0xffff
}

var _ color.Color = lightBackground{}
