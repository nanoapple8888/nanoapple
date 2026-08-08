package setting

// Hupijiao payment configuration. Runtime availability also requires valid
// credentials, a public HTTPS callback address, and payment compliance.
var (
	HupijiaoEnabled   bool
	HupijiaoEndpoint  = "https://api.xunhupay.com/payment/do.html"
	HupijiaoAppID     string
	HupijiaoAppSecret string
)
