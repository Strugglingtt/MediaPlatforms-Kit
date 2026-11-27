package douyin

import (
	"MediaPlatforms-Kit/scraper"
	"os"
	"path/filepath"
	"testing"
)

func TestConfigAndInvalidCookie(t *testing.T) {
	dir := t.TempDir()
	cookie := filepath.Join(dir, "invalid.json")
	if err := os.WriteFile(cookie, []byte("not json"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := scraper.Config{CookiePath: cookie, ProfileDir: filepath.Join(dir, "profile"), QRPath: filepath.Join(dir, "qr.png"), StatusPath: filepath.Join(dir, "status.json"), Headless: true}
	c := New(cfg).(*client)
	login := c.options(true)
	if login.CookiePath != cfg.CookiePath || login.ProfileDir != cfg.ProfileDir || login.QRPath != cfg.QRPath || login.StatusPath != cfg.StatusPath || !login.Headless || !login.NoWaitScan || !login.LoginOnly {
		t.Fatalf("login options: %+v", login)
	}
	if opts := c.options(false); opts.LoginOnly || !opts.NoWaitScan {
		t.Fatalf("scrape options: %+v", opts)
	}
	if opts := New(scraper.Config{WaitForScan: true}).(*client).options(true); opts.NoWaitScan {
		t.Fatalf("blocking login options: %+v", opts)
	}
	if _, err := c.Login(); err == nil {
		t.Fatal("invalid injected cookie should fail before launching browser")
	}
	if _, err := c.Scrape(); err == nil {
		t.Fatal("invalid injected cookie should fail before launching browser")
	}
}
