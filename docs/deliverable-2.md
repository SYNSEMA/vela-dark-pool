# A dark pool on Vela — Deliverable 2

*October 2, 2026. Code: https://github.com/SYNSEMA/vela-dark-pool (release v0.3.1). Running on Horizen's starter kit v0.2.0: https://dark-pool.synsema.app/*

## What it is

When someone needs to sell a large block of tokens, posting the order in public moves the price before the trade happens. Bots front-run it, buyers wait it out. Today the way around that is an OTC desk, which means trusting a middleman who sees every order.

This app is a sealed-bid market for those blocks. A seller deposits the tokens and opens a sale with a minimum price. Buyers deposit, then send their offers encrypted to the enclave. Nobody sees an offer before the close: not the other buyers, not the seller, not us. At the close the enclave ranks the offers, fills them at a uniform price (or pay-as-bid, the seller chooses), settles everyone from escrow and refunds the rest. The chain learns that a sale opened and how much of it sold. Who bid what, and every losing offer, stays inside.

We moved to this app from the payment policy engine of Deliverable 1 for one reason: Vela needs apps people can walk into with their own wallet, and a policy engine is one deploy per owner. A dark pool is one instance that every seller and buyer shares, it doesn't compete with vela-nova, and anyone can reuse it. It is open source and we don't plan to run it as a trading venue.

## What changed since Deliverable 1

- A privacy pass over the app. Error codes on-chain are generic (the detail goes to the Executor's log only), every request reports the same fuel so the fee doesn't reveal which instruction ran, the clearing receipt no longer carries the proceeds, and the app checks invariants after every transition (no matching that mints, no escrow that leaks).
- Built on a newer Synsema guest (v0.6.29). The module is 9.6 MB: the interpreter for Vela's host ABI with the app embedded, no compiler involved. Anyone can rebuild it from the release asset and get the same SHA-256 (`6b737f5c…24cc`).
- Verified end to end today on the starter kit v0.2.0: deploy, a seller deposit (38 s), a sale opened (35 s), two buyers set up and funded, two sealed offers, one above and one below the reserve (about 36 s each), and the close (37 s). 600 of 1,000 tokens sold to the higher offer; the lower one got a full refund and never saw the other offer. No failed requests.

## Local build and emulated TEE vs. live on-chain

We ran this on Horizen's starter kit v0.2.0, the same containers and contracts as the local Docker setup, hosted on a server so the browser console can reach it. What we already know will be different on Base Sepolia or Horizen L3:

- **Deploying is no longer ours.** On the kit anyone with the deployer role deploys in a minute. On the live networks only Horizen deploys, through the intake. So the first time this module runs in a real TEE is after review, and every fix after that is a new deploy and a new app id.
- **The TEE is real.** The kit emulates it; live it is AWS Nitro with real attestation. Nothing in the app changes, but it is the one part we cannot test ourselves before the deploy.
- **Configuration by hand.** Each network has its own ProcessorEndpoint, TEE authenticator, authority service and subgraph. They reached us in a message; there is no registry a client can read. The authority services are plain `http://` on an IP, which a browser app served over HTTPS cannot call directly.
- **Tokens and gas.** Locally the kit mints a test token and funds every account. Live, the payment token is Circle's test USDC on Base Sepolia and every participant needs testnet ETH from a faucet.
- **Gasless needs the facilitator.** Horizen's facilitator makes requests gasless, but it is configured for vela-nova. For our users to bid without gas it would need to accept this app too.

## Where the starter kit and the docs fell short

- Fees. The fuel price lives in the executor's environment and can't be read from anywhere; nothing says how to size `maxFeeValue`. A team on our devnet sent the minimum and got three `INSUFFICIENT_FUEL` in a row.
- A failed request carries no events, no withdrawals and no state change. The docs list "wait" and "decrypt events" as consecutive steps without saying to check the status first.
- `AuthorityNotAllowed` gives no hint that `addAllowedAuthority` is missing, and an event addressed to an unregistered user fails the whole request (code 9).
- The kit's ProcessorEndpoint allows 10 apps; the 11th fails with `MaxNumOfApplicationsExceeded`.
- `errorMessage` is cut at 100 characters, which the docs don't say.
- In the unreleased v0.3.0, removing `APP_NOT_DEPLOYED` renumbers `ErrorCode`, and the ordinal is what gets signed, stored and indexed. A reserved slot would keep existing clients and subgraphs working.

What helped: the Go code is clear, the contracts are readable, the compose comes up with one command, and the TS client shows every field.

## What we'd like from Vela

1. The facilitator open to other apps, so any Vela app can offer gasless requests.
2. The fuel price readable (in the authority service info or on-chain), and a paragraph on sizing fees.
3. A public list of each network's addresses, and the authority services behind HTTPS.
4. A way to test a module in a real TEE before asking for the production deploy, even a short-lived one.
5. A stable `ErrorCode` numbering across versions.

## Comparison

Against Horizen's reference app (vela-nova, Go + TinyGo), the same feature set written in Synsema came to 1,040 lines in 3 files against 3,689 lines in 28, and tests in the same files instead of a separate suite. For the privacy itself, the alternatives we looked at hide orders with MPC or ZK (as in Renegade's dark pool) or use a confidential EVM; Vela's trade-off is a TEE with plain application code, which is what lets the matching logic be one readable file.

## How it is built

The app is one Synsema file, `app/app.syn`, embedded in our published Vela guest. The console and the command-line client are in the same repo. Synsema is a programming language for AI agents; for Vela it means a team can write and test an app without TinyGo or a compiler. Docs: https://synsema.dev/en/0.6.x/73-vela
