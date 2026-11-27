// Package scraper exposes the shared result model and platform scraper interface.
package scraper

// Config contains browser and login dependencies. WaitForScan blocks for up to two
// minutes; by default a QR code and a live session are returned immediately.
type Config struct {
	CookiePath  string
	ProfileDir  string
	QRPath      string
	StatusPath  string
	Headless    bool
	WaitForScan bool
}

// Scraper supports login without collecting videos and independent video collection.
type Scraper interface {
	Login() (*Result, error)
	Scrape() (*Result, error)
}
