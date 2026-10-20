package fixture

// SeedExclusion is the content-type exclusion Marasi seeded as app.filters in
// 00001_db_setup.sql, which marasi-app uses until a tester edits it. The
// benchmarks call it "the seed exclusion". It is copied here because the
// project no longer stores the seeded default once client settings replace
// app.filters (docs/adr/2026-10-14-client-settings-are-opaque.md). The excluded stretches use only its types.
var SeedExclusion = []string{
	"audio/aac", "image/apng", "image/avif", "video/x-msvideo", "image/bmp",
	"audio/midi", "audio/x-midi", "image/gif", "image/vnd.microsoft.icon",
	"image/jpeg", "audio/mpeg", "video/mp4", "video/mpeg", "audio/ogg",
	"video/ogg", "image/png", "image/svg+xml", "image/tiff", "video/mp2t",
	"audio/wav", "audio/webm", "video/webm", "image/webp", "video/3gpp",
	"audio/3gpp", "video/3gpp2", "audio/3gpp2", "application/vnd.ms-fontobject",
	"font/otf", "font/ttf", "font/woff", "font/woff2", "application/x-abiword",
	"application/vnd.amazon.ebook", "application/x-cdf", "application/x-csh",
	"application/epub+zip", "application/vnd.apple.installer+xml",
	"application/vnd.oasis.opendocument.presentation",
	"application/vnd.oasis.opendocument.spreadsheet",
	"application/vnd.oasis.opendocument.text", "application/vnd.visio",
	"application/vnd.mozilla.xul+xml", "application/javascript",
	"application/octet-stream", "binary/octet-stream", "image/x-icon",
	"image/vnd.djvu", "image/x-portable-pixmap", "image/x-portable-bitmap",
	"audio/basic", "audio/aiff", "audio/x-aiff", "audio/x-wav",
	"audio/x-mpegurl", "audio/x-ms-wma", "video/quicktime", "video/x-flv",
	"video/x-ms-wmv", "video/x-msvideo", "application/pdf", "application/msword",
	"application/vnd.ms-excel", "application/vnd.ms-powerpoint",
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	"application/vnd.openxmlformats-officedocument.presentationml.presentation",
	"application/rtf", "application/x-rar-compressed",
	"application/x-7z-compressed", "application/zip", "application/x-tar",
	"application/gzip", "application/x-font-ttf", "application/x-font-woff",
	"text/css", "text/calendar", "text/vcard", "application/x-shockwave-flash",
	"application/x-bzip", "application/x-bzip2",
}
