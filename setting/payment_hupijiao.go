package setting

// Hupijiao payment configuration. The gateway is enabled when all three
// values are configured and payment compliance has been confirmed.
var (
	HupijiaoEndpoint  = "https://api.dpweixin.com"
	HupijiaoAppID     string
	HupijiaoAppSecret string
)
