// Blob backend registration.
//
// gocloud.dev registers a URL opener per scheme through package initialisation, so a
// backend is only reachable if its driver package is linked in. All three supported
// schemes are registered here, in one file, because the alternative — registering from
// each main — would let a binary silently lack a backend its configuration names.
//
// The cost is dependency weight: s3blob links the AWS SDK and gcsblob links the Google
// Cloud storage client. That weight buys one code path for three backends, which is the
// trade D15 accepted. Trimming a binary means dropping an import here, and the failure
// mode is then a clear "no driver registered for scheme" at open time.
package artifact

import (
	_ "gocloud.dev/blob/fileblob" // file://
	_ "gocloud.dev/blob/gcsblob"  // gs://
	_ "gocloud.dev/blob/s3blob"   // s3://
)
