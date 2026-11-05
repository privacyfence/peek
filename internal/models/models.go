package models

import "time"

const (
	UploadVisibilityPublic   = "public"
	UploadVisibilitySignedIn = "signed-in"

	// LegacyVisibilityPrivate is the former name of UploadVisibilitySignedIn. It is
	// still accepted as input and rewritten by migration 14.
	LegacyVisibilityPrivate = "private"

	UploadVisibilityRestricted = "restricted"
)

const (
	UploadGrantUser  = "user"
	UploadGrantGroup = "group"
)

type UploadGrant struct {
	ID                 int64
	UploadID           int64
	Kind               string // UploadGrantUser or UploadGrantGroup
	Email              string // lowercased
	CreatedByAccountID int64
	CreatedAt          time.Time
}

type Token struct {
	ID        int64
	AccountID int64
	Token     string
	Name      string
	Email     string
	IsAdmin   bool
	Disabled  bool
	CreatedAt time.Time
	ExpiresAt int64 // unix timestamp, 0 = no expiry

	LastUsedAt int64 // unix seconds of the last recorded API use, 0 = never
}

// APIKey is one of an account's own bearer tokens as the dashboard lists it.
// It never carries the secret or its hash.
type APIKey struct {
	ID         int64
	AccountID  int64
	Name       string // tokens.name
	Hint       string // last four characters of the key, "" when unknown
	CreatedAt  time.Time
	ExpiresAt  int64 // unix seconds, 0 = never
	LastUsedAt int64 // unix seconds, 0 = never
}

type Account struct {
	ID           int64
	Email        string
	Name         string
	PasswordHash string
	IsAdmin      bool
	Disabled     bool
	SessionEpoch int64 // bumped to end every dashboard session of the account
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type OAuthIdentity struct {
	ID             int64
	AccountID      int64
	Provider       string
	ProviderUserID string
	Email          string
	Name           string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type Invite struct {
	ID                 int64
	Token              string
	Email              string
	CreatedByAccountID int64
	CreatedAt          time.Time
	ExpiresAt          time.Time
	UsedAt             time.Time
	RevokedAt          time.Time
}

type CLILoginDevice struct {
	ID         int64
	UserCode   string
	Status     string
	AccountID  int64
	CreatedAt  time.Time
	ExpiresAt  time.Time
	ConsumedAt time.Time
	RequestIP  string
	UserAgent  string
}

type Upload struct {
	ID             int64
	Slug           string
	OwnerTokenID   int64
	OwnerAccountID int64
	OwnerName      string
	Name           string
	Size           int64
	Visibility     string
	CreatedAt      time.Time
	LatestVersion  int
}

type UploadVersion struct {
	ID                 int64
	UploadID           int64
	Version            int
	ObjectKey          string
	Size               int64
	CreatedByAccountID int64 // 0 when unknown (backfilled rows)
	CreatedAt          time.Time
}

// UploadBackend is the container image a page version runs as its backend.
type UploadBackend struct {
	UploadID           int64
	Version            int
	Image              string
	Port               int
	CreatedByAccountID int64 // 0 when unknown
	CreatedAt          time.Time
}

// BackendEnvVar is one environment variable of a page's backend. It applies to
// every version of the page.
type BackendEnvVar struct {
	UploadID  int64
	Name      string
	Value     string // ciphertext as stored; the server decrypts
	UpdatedAt time.Time
}

// BackendTokenCall counts one account's API-token calls to one page version's backend.
type BackendTokenCall struct {
	UploadID   int64
	Version    int
	AccountID  int64
	Email      string // the account's email; "" for automation accounts
	Calls      int64
	LastCallAt time.Time
	LastStatus int  // HTTP status the backend answered the last call with
	Identified bool // some call was made while the page was signed-in or restricted, so the backend got the account's identity
}

type Comment struct {
	ID              int64
	UploadID        int64
	ElementSelector string
	ElementText     string
	AnchorKind      string
	AuthorName      string
	AuthorCookie    string
	AccountID       int64 // 0 when the author name was typed, not a signed-in account
	Body            string
	Version         int
	CreatedAt       time.Time
}

type Visit struct {
	ID            int64
	UploadID      int64
	VisitorCookie string
	VisitorName   string
	IP            string
	UserAgent     string
	Version       int
	VisitedAt     time.Time
}

type Visitor struct {
	Cookie    string
	Name      string
	CreatedAt time.Time
	LastSeen  time.Time
}

type Stats struct {
	Slug           string
	Name           string
	TotalVisits    int
	UniqueVisitors int
	Recent         []Visit
}

type VisitBucket struct {
	Time  time.Time
	Count int
}

// Provisioner is a key holder that may create, adopt and release accounts through the provisioning API.
type Provisioner struct {
	ID                 int64
	Name               string
	Hint               string
	CreatedByAccountID int64
	CreatedAt          int64
	LastUsedAt         int64
	Accounts           int64 // number of links; filled by ListProvisioners only
}

// ProvisionedAccount is one provisioner's link to one account.
type ProvisionedAccount struct {
	ProvisionerID  int64
	ExternalID     string
	AccountID      int64
	CreatedAccount bool
	TokenID        int64 // 0 when the link holds no token
	TokenExpiresAt int64 // the held token's expires_at (0 = never or no token)
	CreatedAt      int64
	UpdatedAt      int64
}
