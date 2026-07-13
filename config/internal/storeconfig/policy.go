package storeconfig

// Operational policies: deletion, GC coordination, session overrides and
// read-time verification.

// DeletionPolicy is the deletion policy.
type DeletionPolicy string

const (
	DeletionPolicyNoDelete  DeletionPolicy = "NoDelete"
	DeletionPolicyRetention DeletionPolicy = "Retention"
	DeletionPolicyFree      DeletionPolicy = "Free"
)

// GCLeasePolicy is the policy for GC Agent coordination.
type GCLeasePolicy string

const (
	GCLeaseAuto           GCLeasePolicy = "Auto"
	GCLeaseSingleHost     GCLeasePolicy = "SingleHost"
	GCLeaseLeaderElection GCLeasePolicy = "LeaderElection"
)

// SessionOverridesPolicy is the admin knob over class-III client
// overrides (ADR-110): Allow (default) lets a connection carry its own
// session preferences; Deny refuses any class-III divergence the same
// way class II is refused.
type SessionOverridesPolicy string

const (
	SessionOverridesAllow SessionOverridesPolicy = "Allow"
	SessionOverridesDeny  SessionOverridesPolicy = "Deny"
)

// VerifyOnReadPolicy controls explicit ContentHash verification on Get.
type VerifyOnReadPolicy string

const (
	VerifyOnReadAuto         VerifyOnReadPolicy = "Auto"
	VerifyOnReadForceEnabled VerifyOnReadPolicy = "ForceEnabled"
	VerifyOnReadDisabled     VerifyOnReadPolicy = "Disabled"
)
