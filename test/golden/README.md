# Golden byte sequences

Fixed byte sequences of TWAMP messages and packets, transcribed field by field by hand from the RFC figures
(RFC 4656 §3.1, §3.5, §3.7, §4.1.2; RFC 5357 §3.5, §3.8, §4.2.1; RFC 6038 §4.2/§4.3; RFC 7750 §2.2).
They were written independently of the encoder code, and the golden tests in `internal/twamp/wire` check that
"decoding yields the expected values" and "encoding the expected values yields exactly these bytes".

## Format (`*.hex`)

- Two hexadecimal digits per octet. Whitespace and newlines are ignored.
- `#` starts a comment that runs to the end of the line. The leading comment on each line gives the offset
  in the RFC figure and the field name.
- Values are typical ones seen in interoperability testing (padding 27, Timeout 2s, DSCP EF, and so on), using
  a different pattern for every octet so that swapped fields are detected.

## Authenticated-mode cryptography (`auth-vectors.txt`)

Expected values for the Authenticated / Encrypted / Mixed modes: key derivation (PBKDF2-HMAC-SHA1, 16 octets),
Token (AES-128-CBC, IV 0), test session keys (AES-ECB / CBC from the SID), test packet HMAC and encryption, and the
control stream (CBC chaining from Client-IV / Server-IV and the HMAC coverage). `auth-vectors.py` computes them
independently of gotwamp with Python's hashlib / hmac and the openssl command. One value per line as
`name hex`; lines starting with `#` are comments.

```sh
python3 test/golden/auth-vectors.py > test/golden/auth-vectors.txt   # regenerate (inputs are fixed)
```

## M7 extensions (RFC 6038 / RFC 5938 / STAMP)

The following files, transcribed by hand from the RFC figures, are checked by `internal/twamp/wire/golden_ext_test.go`.

- `start-n-sessions.hex` / `stop-n-ack.hex`: the Individual Session Control messages of RFC 5938 §3.2 / §3.5
- `sender-packet-symmetrical.hex` / `reflector-packet-symmetrical.hex`: Symmetrical Size per RFC 6038 §5.1.4 / §5.2.2
  (the sender's 27-octet MBZ and the padding returned unchanged)
- `stamp-sender.hex` / `stamp-reflector.hex`: the STAMP base packets of RFC 8762 §4.2.1 / §4.3.1 and the SSID / TLV of RFC 8972
