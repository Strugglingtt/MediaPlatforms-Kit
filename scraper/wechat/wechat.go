// Package wechat provides the WeChat Channels creator scraper.
package wechat

import "MediaPlatforms-Kit/scraper"

type client struct{ config scraper.Config }

var _ scraper.Scraper = (*client)(nil)

func New(config scraper.Config) scraper.Scraper { return &client{config: config} }

func (c *client) options(loginOnly bool) WechatOptions {
	return WechatOptions{
		CookiePath: c.config.CookiePath,
		ProfileDir: c.config.ProfileDir,
		QRPath:     c.config.QRPath,
		StatusPath: c.config.StatusPath,
		Headless:   c.config.Headless,
		NoWaitScan: !c.config.WaitForScan,
		LoginOnly:  loginOnly,
	}
}

func (c *client) Login() (*scraper.Result, error) {
	return ScrapeWechat(c.options(true))
}

func (c *client) Scrape() (*scraper.Result, error) {
	return ScrapeWechat(c.options(false))
}
