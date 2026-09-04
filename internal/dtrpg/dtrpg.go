// Package dtrpg wraps github.com/pilgrimagesoftware/dtrpg-sdk.go for the CLI's
// `import dtrpg` commands: application-key storage in the OS keychain, session
// exchange, paged library retrieval, and product-to-volume mapping. It drives
// catalog-api only through the internal/client package, never directly.
package dtrpg

// Keychain identifiers. The service name is shared with the platform session
// store; the account keeps the DriveThruRPG key in its own slot so `auth
// logout` and `import dtrpg logout` never touch each other's credentials.
const (
	// KeychainService matches auth.ServiceName. Duplicated as an untyped
	// constant to avoid importing internal/auth from here.
	KeychainService = "sweetrpg-catalog-cli"
	// KeychainAccount is the slot holding the DriveThruRPG application key.
	KeychainAccount = "dtrpg-app-key"
)

// Volume property names carrying DriveThruRPG provenance. dtrpg_product_id is
// also the idempotency key: a second import skips any product whose ID already
// appears here on an existing volume.
const (
	PropProductID      = "dtrpg_product_id"
	PropOrderProductID = "dtrpg_order_product_id"
	PropPurchaseDate   = "dtrpg_purchase_date"
	PropCoverURL       = "dtrpg_cover_url"
	PropISBN           = "dtrpg_isbn"
)

// imageBaseURL is the prefix for the relative cover-image paths the DTRPG API
// returns on product metadata.
const imageBaseURL = "https://api.drivethrurpg.com/images/"
