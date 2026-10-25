#!/bin/sh
# Builds app/app.syn into a Vela guest module: build/app.wasm (+ .sha256). No compiler involved.
#
# The module is Synsema's interpreter plus your program. Every Synsema release publishes the
# interpreter as a Vela guest (`synsema-vela-guest.wasm`, an asset of the release) with an app slot
# inside; scripts/embed.syn puts your program into that slot. The SHA-256 Vela verifies on-chain
# covers interpreter and program together, so the guest is checked against the hash pinned below,
# written in this repository, never against a .sha256 from the same place it was downloaded from.
# README.md, "Rebuild the module from source", builds the same guest from Synsema's source.
# Needs the `synsema` binary and curl.
#
#   sh scripts/build.sh                          # app/app.syn with the pinned guest (downloaded once into .synsema/)
#   APP=path/to/other.syn sh scripts/build.sh
#   GUEST_WASM=/path/to/synsema-vela-guest.wasm sh scripts/build.sh   # a guest you built yourself (checked too)
#   SYNSEMA_TAG=<tag> GUEST_SHA256=<its sha256> sh scripts/build.sh    # another release, with its hash
set -eu
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SYNSEMA_TAG="${SYNSEMA_TAG:-v0.6.47}"
GUEST_SHA256="${GUEST_SHA256:-133d0f13ea214e7ca572d7c169e0e57b97057df6eb9f832c6ea036d53ac46cb0}"
APP="${APP:-$ROOT/app/app.syn}"
GUEST_WASM="${GUEST_WASM:-$ROOT/.synsema/synsema-vela-guest-$SYNSEMA_TAG.wasm}"

if [ ! -f "$GUEST_WASM" ]; then
  echo "downloading the Vela guest of Synsema $SYNSEMA_TAG …"
  mkdir -p "$(dirname "$GUEST_WASM")"
  curl -fsSL -o "$GUEST_WASM.part" "https://github.com/kitecosmic/synsema/releases/download/$SYNSEMA_TAG/synsema-vela-guest.wasm"
  mv "$GUEST_WASM.part" "$GUEST_WASM"
fi
GOT="$(sha256sum "$GUEST_WASM" | cut -d' ' -f1)"
[ "$GOT" = "$GUEST_SHA256" ] || { echo "the guest is not the pinned one: $GUEST_WASM has sha256 $GOT, wanted $GUEST_SHA256"; exit 1; }

# embed.syn reads and writes inside the repository: the inputs are copied next to the output.
mkdir -p "$ROOT/build"
NAME="$(basename "$APP")"
cp "$GUEST_WASM" "$ROOT/build/guest.wasm"
[ "$APP" -ef "$ROOT/build/$NAME" ] || cp "$APP" "$ROOT/build/$NAME"
cd "$ROOT"
synsema run scripts/embed.syn -- build/guest.wasm "build/$NAME" build/app.wasm
rm -f build/guest.wasm
( cd build && sha256sum app.wasm > app.wasm.sha256 )
echo "guest $SYNSEMA_TAG sha256 $GUEST_SHA256"
echo "next: node scripts/smoke.mjs build/app.wasm   (optional; Node 20 or 24+, not 22)"
