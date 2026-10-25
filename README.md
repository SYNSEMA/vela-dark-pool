# vela-dark-pool — a dark pool for block trades on Vela (Horizen), in Synsema

A dark pool: orders nobody sees before the close, so nothing to front-run. A seller offers a block
of one token for another (an OTC block, a token sale, a bill); buyers deposit the payment token and
send their orders **encrypted to the enclave**. Nobody — not the other buyers, not the seller, not
an auditor — sees an order before the close. The matching runs inside, deterministically; winners
and losers are settled from their escrow. Each winner learns its own fill and what it paid, each
loser only its refund, and the losing orders are then dropped: they reach no one. The chain sees
that a block was offered and how much of it sold — and, like every Vela app, who sends each request
and when, and the token movements in and out ([what an observer can work out](#what-an-observer-can-work-out)).
Mechanically it is a sealed-bid batch auction (uniform price or pay-as-bid), which is what the code
calls it: `open`, `bid`, `close`.

```
seller ───open (asset, quantity, payment, reserve, kind)──▶ ┌────────── enclave ──────────┐──▶ chain: opened(id, asset, quantity, payment, kind)
bidder ───bid (quantity, total), sealed────────────────────▶ │ escrow · the book · ranking   │
seller ───close───────────────────────────────────────────▶ │ matching · settlement        │──▶ chain: cleared(id, sold, kind)
                                                             └──────────────────────────────┘──▶ each winner: its fill, what it paid · each loser: its refund
anyone ───withdraw (a pull-payment) · claim-for on-chain                                       ──▶ the seller: sold, proceeds, how many won
auditor ──audit ──▶ balances and finished results (never an open book)
```

Built on [Vela](https://docs.horizen.io/vela/introduction/), whose Executor runs the app inside a
TEE and settles every result on-chain. The app is **one `.syn` file** with its tests; the module is
the release's guest with that program in its slot (no compiler); and the side outside the enclave is
Synsema too: a **web console** (the recipe's entry) and a command-line client on the same library.
Tested on Vela's own runtime (`tests/vela`, Vela's `pkg/wasm`) and end to end against Horizen's
starter kit v0.2.0 (the real Executor, the real contracts) on the public devnet.

## The console

Deploy the recipe on [synsema.com](https://synsema.com) — the project's environment is provisioned
from the public devnet at creation (a token of your own, the addresses, the keys) — or run it
locally: `synsema serve web.syn` from this folder, with `.env` copied from `.env.example` and filled
by `cd client && synsema run vela_client.syn -- devnet`. Then, in the browser:

1. **Deploy the auction house.** The console embeds `app/app.syn` into the release's guest module
   (checked against its pinned hash), deploys it to Vela and registers the seller's key with the
   enclave.
2. **Deposit the asset** the seller will sell: the stack's token (`VELA_TOKEN`).
3. **Set up bidders' desks.** On a devnet the console plays every side: a bidder is a wallet it
   custodies, with a P-521 key of its own, given some ETH from the seller's wallet; it registers and
   deposits by itself, and its bids are signed with its own key and sealed with its own P-521 key.
   The enclave treats it exactly as a bidder with a wallet of their own, who uses the command-line
   client and never sees this page.
4. **Open an auction**: a quantity of the token for ETH, a reserve for the whole lot, uniform price
   or pay-as-bid.
5. **Bid from each desk.** A quantity and a total; the desk shows the price it means and sends the
   bid encrypted. Nobody — not the seller, not the other desks — sees it before the close.
6. **Close.** The matching runs inside. Each winning desk learns its fill and what it paid, each
   losing desk its refund; the seller learns how much sold, the proceeds and how many won; the
   chain learns how much sold.
7. **Withdraw and claim**: what was won, what came back, the proceeds — pull-payments claimed on-chain
   from the same button.

Every action is one request to the enclave: 30 to 60 seconds on a devnet. The console's state
(app id, keys, desks, auctions) lives in `data/auction.json`, a volume on the platform.

**It is a single-operator demo.** The console holds every desk's keys, in clear, in
`data/auction.json`: whoever reads that file, or reaches the page, can act for every side and read
every desk's results. It listens on `127.0.0.1` unless `CONSOLE_BIND` says otherwise, and with
`CONSOLE_PASSWORD` set every page asks for that password first (a session cookie). The public demo
on the platform runs without a password on devnet money and says so on every page. A real bidder
uses the command-line client with a wallet and keys of their own.

## What you get

```
web.syn                      the console: deploy · deposit · desks · open · bid · close · results · withdrawals (the recipe's entry, kind = web)
pages/                       its pages: the seller's desk, a bidder's desk, the sign-in
app/app.syn                  the book, inside the enclave: deploy · deposit · open · bid · cancel_bid · close · cancel · withdraw · deanonymize, with tests
tests/vela/                  the module on Vela's own runtime (pkg/wasm): policy, labels, caps, what each party learns, the state's size
client/vela_lib.syn          Vela's client protocol as a module (keys, cipher, padding, submit, events, facilitator, reports, token amounts)
client/vela_client.syn       the seller's, the bidders' and the auditor's commands, on top of the module
scripts/embed_lib.syn        the app slot of a guest module (what build.sh and the console use to embed the program)
scripts/erc20/               the test token (TST, 6 decimals, permit) scripts/devnet.sh deploys and allowlists locally
scripts/build.sh             app/app.syn → build/app.wasm (the pinned guest with your program in its slot) + sha256
scripts/embed.syn            puts a .syn into the app slot of a guest module — what build.sh runs; no compiler
scripts/smoke.mjs            probe of the module under Node's WASI, the way the Executor drives it
scripts/devnet.sh            Horizen's starter kit in Docker + the test token; client/.env written
scripts/e2e.sh               a whole auction: deploy → four parties → open → two sealed bids → close → results → claims → audit
.github/workflows/build.yml  CI: tests, provenance of the binary and the guest, build, Node probe, Vela's runtime, build/app.wasm as an artifact
syn.toml                     the recipe descriptor: the console as entry, the public devnet as default, [provision] for the token
```

## How the matching works

- A bid is a pair: `quantity` of the asset for `total` of the payment token. The price is the
  ratio, and bids are ranked by cross-multiplication — exact integers, never a float. Ties go to
  the earlier bid.
- `reserve` is the least total the seller accepts for the whole quantity, a price floor; bids
  below it are never filled.
- **Uniform price** (default): the best bids win until the quantity runs out; every winner pays the
  price of the lowest accepted bid (the clearing price); the last winner may be partly filled and
  pays `floor(filled × total ÷ quantity)` at that price. **Pay-as-bid**: each winner pays its own.
- The rest of every bidder's escrow comes back to its balance; the unsold remainder goes back to
  the seller; the proceeds land in the seller's balance. Everyone withdraws as a pull-payment,
  claimed on-chain by anyone (`claim-for`).
- One live bid per bidder per auction; a new one replaces the old; `cancel-bid` before the close.
  The seller can `cancel` an open auction: every bid is refunded.
- Limits, so the state (one document of at most 10 MiB that Vela passes to every call) cannot be
  grown until nothing closes: 200 bids per book, 3 open auctions per seller and 20 in all, the
  last 50 finished auctions kept (winners only), and an emptied balance leaves no row. The worst
  case, 20 full books, is measured in `tests/vela` (`DARKPOOL_FULL=1`): 437 KB of state, 2.1 s for
  the slowest request while filling them, 15 s to close a book of 200 bids (Vela's runtime, one CPU).
- Amounts are whole numbers of the token's smallest unit, as decimal text that fits a `uint256`;
  anything else (`"1.5"`, `"-1"`, `"0x10"`) is refused with `bad_amount`.

## Deploying

The module deploys with **no constructor parameters**: the app's private output policy is its
default (`POLICY` in `app/app.syn`), kept by the adapter inside the signed state:

- `reject: "private"` — a refused request (a bid larger than the balance, a seller bidding on their
  own auction) is an ordinary success on-chain, and the reason goes encrypted to the sender only;
- `events_pad: 256` — every event, and the state, is padded to 256-byte steps, so a winner's result
  and a loser's have the same size;
- `events_min: 2` — every result has at least two events, and the app rounds the events of a close
  or a cancel up to a power of two;
- `fuel: "80"` — one fee for every instruction, declared, so it does not name the instruction and no
  literal elsewhere in the source can change it.

The clients pad every payload to 256 bytes before sealing it (`vela_lib.padded`), so the size of a
request does not say which instruction it is or how many digits a bid has.

**Deanonymization.** The app supports Vela's `Deanonymize` request, which only an address on the
app's authority allowlist (`DefaultAuthority.addAllowedAuthority`) can send and whose report Vela
encrypts to the requester. Two report types:

| `report_type` | contains |
|---|---|
| `audit` (default) | every balance; every finished auction with its result: quantity sold, proceeds, each winner and what it paid; of an open auction only what its public receipt said |
| `auction` + `auction: <id>` | the same, for one auction |

No report contains an open book, and none contains a losing bid: those are dropped at the close.

## Ten minutes, from the terminal

You need the [`synsema` binary](https://synsema.org) (`npm i -g synsema`, or the install script). That is
all: the module is the release's guest with your program in its slot — no compiler, a few seconds.
For the stack you need Docker, or a token of your own on the public devnet (`synsema run vela_client.syn -- devnet`).

```sh
synsema test --labels app/app.syn        # 1. the book, natively — the same code runs in the enclave
sh scripts/build.sh                      # 2. build/app.wasm: the pinned guest + your program (the guest downloads once)
(cd tests/vela && go test ./...)         #    the module on Vela's runtime (Linux, Go 1.24, a C compiler)
node scripts/smoke.mjs build/app.wasm    #    Node 20 or 24+ (not 22)
sh scripts/devnet.sh                     # 3. Vela in Docker + the test token; writes client/.env
sh scripts/e2e.sh                        # 4. a whole auction with four parties, claims checked on-chain
```

`scripts/e2e.sh` deploys the app, registers the seller (the signing key, Anvil #0 on the kit), two
bidders (Anvil #1 and #2 by default) and an auditor (Anvil #3) — on the public devnet, which refuses
the built-in accounts, each of them gets a token of its own (`vela_client.syn -- account`) — funds them, opens an auction of 1000
of `VELA_TOKEN` for ETH with a 0.5 ETH reserve, places two sealed bids (600 for 0.9 ETH and 600 for
0.6 ETH), closes it — the first bidder gets 600, the second 400, both at the clearing price of
0.001 ETH per unit — claims the withdrawals on-chain, checking the balances, and has the auditor,
who is neither the seller nor a bidder, read the auction's report. About twelve minutes on the
public devnet.

## Rebuild the module from source

On Vela an app's code identity is the SHA-256 of its module, and the module is the whole Synsema
interpreter (the guest) with `app/app.syn` in a slot. Three checks, from the quickest:

1. **The guest is the pinned one.** `scripts/build.sh` and the console compare the downloaded guest
   with the hash written in this repository (`GUEST_SHA256`), not with a `.sha256` from the same
   release. Then `scripts/embed.syn` fills the slot, byte for byte as Synsema's own build does.
2. **The guest was built by Synsema's release workflow, from its tag.** Each release asset carries a
   build provenance attestation signed through GitHub's Sigstore instance; CI checks it on every run:

   ```sh
   gh attestation verify .synsema/synsema-vela-guest-v0.6.47.wasm --repo kitecosmic/synsema \
     --signer-workflow kitecosmic/synsema/.github/workflows/release.yml
   ```

3. **Build the guest yourself.** The guest embeds the absolute paths of its build (Rust panic
   locations), so it reproduces byte for byte only in the directories GitHub Actions uses — Horizen's
   review rebuilt v0.6.29 this way and got the published hash exactly:

   ```sh
   # in /home/runner/work/synsema/synsema (a checkout of kitecosmic/synsema at the guest's tag),
   # with CARGO_HOME=/home/runner/.cargo and rustc 1.99.1 (rust-toolchain.toml pins it)
   SYNSEMA_VERSION=v0.6.47 cargo build --locked --manifest-path packages/guests/vela/Cargo.toml \
     --profile wasm --target wasm32-wasip1 --target-dir engine/target
   cargo run --locked --manifest-path packages/guests/vela/tools/wasi-stub/Cargo.toml -- \
     engine/target/wasm32-wasip1/wasm/synsema_vela_guest.wasm synsema-vela-guest.wasm
   sha256sum synsema-vela-guest.wasm     # 133d0f13ea214e7ca572d7c169e0e57b97057df6eb9f832c6ea036d53ac46cb0
   GUEST_WASM=$PWD/synsema-vela-guest.wasm sh /path/to/vela-dark-pool/scripts/build.sh
   ```

`build/app.wasm.sha256` is then the module's identity, the hash to compare with the deployed app.

## The commands

`client/vela_client.syn`, run from `client/`; whoever holds `VELA_SECP_KEY` in `.env` is the caller.
Amounts are in tokens; `eth` names the native token.

| `synsema run vela_client.syn -- …` | who | does |
|---|---|---|
| `fund <eth\|token> <tokens>` | anyone | deposits into your balance in the app (approves first for an ERC-20) |
| `open <asset> <quantity> <payment> [reserve] [uniform\|pay_as_bid]` | the seller | locks the asset, opens the book (the receipt names the offer; the request names its sender) |
| `bid <n> <quantity> <total>` | a bidder | a sealed bid; the client shows the price it means and sends it encrypted |
| `cancel-bid <n>` | a bidder | before the close; the escrow comes back |
| `close <n>` · `cancel <n>` | the seller | the matching, or a refund of every bid |
| `results [n]` | anyone | your decrypted events: your bids, your result (fill and what you paid, or your refund), the seller's summary |
| `auctions [n]` | anyone | the public events decoded: `opened`, `cleared` |
| `withdraw <eth\|token> <tokens> [to]` · `pending` · `claim-for` · `token-balance` | anyone | balances out as pull-payments, claims, on-chain balances |
| `audit ['<json>']` | an allowed authority | balances and finished results; `{"report_type":"auction","auction":"0x…01"}` for one |
| `devnet` · `allow-token` · `allow-authority` | anyone | a token of your own on the public devnet; allowlists with the admin key |

Plus the starter kit's generic commands (`keys`, `register`, `deploy`, `send`, `events`, the
facilitator flow). An auction is named by its number (`1`) or its id (`0x…01`).

## What an observer can work out

Vela encrypts the **contents** of every request and every event. It does not hide **who** sends a
request or **when**, it cannot hide token movements, and the number and size of what each request
produces are on-chain. With the private policy and padded payloads, this is what someone watching
the chain learns, and what stays inside.

**Never leaves the enclave:** the price and size of any bid, the ranking, who lost, what anyone
paid, a refused request's reason (to anyone but its sender), the balances.

**Public by construction:**

- **The seller of each auction.** `open` and `close` produce the public receipts `opened` and
  `cleared`, and the request's sender is public, so anyone can read "this wallet offered 1000 TST"
  and "this wallet closed it".
- **The offer and how much of it sold.** `opened` carries the asset, the quantity, the payment token
  and the pricing kind; `cleared` the quantity sold. The quantity sold is published on purpose: it is
  the headline of a block trade and what makes the venue auditable. Know what it costs: **when a
  single bidder takes the whole lot, the quantity sold is that bidder's quantity**, and a partial
  clear tells you the winning side's total size. If your trade cannot afford that, split the lot
  across auctions.
- **Which wallets use the app, and when.** Every request shows its wallet and time. With padded
  payloads an observer cannot tell a bid from a withdrawal or a cancellation, nor, when several
  auctions are open, which one a request is for — but they see which wallets are active.
- **Roughly how many bid.** A close or a cancel emits one event per bidder plus the seller's,
  rounded up to a power of two: 1 bidder looks like any other request (2 events), 2 or 3 like 4,
  100 like 128.
- **Deposits and withdrawals**, with their wallet, token and amount. A withdrawal names no auction
  and can happen at any time, but it can still give a fill away: a wallet that is not a seller and
  withdraws more of a token than it deposited won at least that much of it; with one auction and one
  close, a winner's withdrawals right after it can give the price too (600 TST and 0.28 ETH out of
  1 ETH in: 600 for 0.72). With several auctions of the same token, or withdrawals spread over
  time, this gets much harder. A seller is public, so its withdrawals of the payment token after a
  close are likely proceeds.

**To blur what is public:** withdraw later, and in amounts that do not match a fill (leave a part
in the app for the next auction); use a fresh wallet per bid — knowing that a fresh wallet has to
be funded on-chain, and its funding can link it back to its owner; and expect the size of a sole
winner to be the quantity sold.

## Gotchas

- Every party that receives events — the seller, every bidder — registers first (`register`); an
  event for an unregistered address fails the request.
- Both tokens must be on Vela's allowlist (`allow-token` with the admin key on a devnet); ETH always is.
- Deploying needs `DEPLOYER_ROLE`; the auditor needs `DefaultAuthority.addAllowedAuthority(appId, address)`
  (`vela_client.syn -- allow-authority <appId> <address>`; on the devnet `VELA_ADMIN_URL` signs it for you).
- This app has no trigger contract: a `TRUSTPROCESS` request is refused with `no_trigger`.
- Amounts are the token's smallest unit as text inside the enclave; the client converts. Integers
  in Synsema multiply and compare exactly at any size, but `/` goes through a float: the app and
  the client divide with their own long division.
- Node 22 crashes intermittently inside V8 running this module, with or without
  `--no-wasm-dynamic-tiering`; use Node 20 or 24+.
- On Windows, run the scripts from Git Bash; `tests/vela` runs on Linux only (Vela's runtime uses
  named pipes for the guest's logs).

The full reference is the docs page [Vela (Horizen)](https://synsema.dev/en/0.6.x/73-vela); the
adapter lives in [kitecosmic/synsema — packages/guests/vela](https://github.com/kitecosmic/synsema/tree/main/packages/guests/vela).

## Guía rápida (español)

Subasta de sobre cerrado: la parte vendedora ofrece un bloque de un token a cambio de otro; las
pujas van cifradas al enclave y nadie las ve antes del cierre, ni siquiera un auditor; el matching
corre adentro (precio uniforme o pay-as-bid, ranking exacto por multiplicación cruzada); la
liquidación sale del escrow. Cada ganador recibe su asignación y lo que pagó, cada perdedor solo su
reintegro, y las pujas perdedoras se descartan. La cadena muestra quién manda cada pedido y cuándo,
la oferta y cuánto se vendió, y los depósitos y retiros (ver "What an observer can work out"). La
app despliega sin parámetros con su política privada por defecto. La consola web (`synsema serve
web.syn`, o la receta en synsema.com con el entorno aprovisionado desde el devnet público) es una
demo de un solo operador: guarda las claves de todas las mesas; escucha en 127.0.0.1 salvo
`CONSOLE_BIND` y pide clave con `CONSOLE_PASSWORD`. Desde la terminal: 1. `synsema test --labels
app/app.syn`. 2. `sh scripts/build.sh` (y `cd tests/vela && go test ./...` en Linux, sobre el
runtime de Vela). 3. `sh scripts/devnet.sh` (o `synsema run vela_client.syn -- devnet`).
4. `sh scripts/e2e.sh`: cuatro partes, una subasta, dos pujas, cierre, resultados, retiros
verificados en cadena y auditoría. Referencia completa en [synsema.dev/es/0.6.x/73-vela](https://synsema.dev/es/0.6.x/73-vela).

## License

Apache-2.0.
