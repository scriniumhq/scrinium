package manifestcodec

// Test-visible aliases. The aliasing keeps the production types
// and helpers unexported while letting external tests
// (manifestcodec_test package) verify the §7.1 header layout.

type FileHeader = fileHeader

var (
	WriteHeader    = writeHeader
	ReadHeader     = readHeader
	CryptoFlag     = cryptoFlag
	CryptoFromFlag = cryptoFromFlag
	MagicJSON      = magicJSON
)

const (
	CryptoPlainFlag    = cryptoPlain
	CryptoSealedFlag   = cryptoSealed
	CryptoParanoidFlag = cryptoParanoid
)
