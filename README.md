# OptionsHedger

OptionsHedger is a high-performance **options hedging engine** for BTC options on Deribit.  
It is designed with **low latency**, **atomic memory updates**, and **FIX protocol integration**, making it suitable for systematic hedging and HFT (high-frequency trading) strategies.

---

## Architecture Overview

The system is composed of five key components:

1. **FIX Layer**  
   Subscribes to option market data + BTC index via Deribit FIX API.
   
2. **REST Layer**  
   Provides authentication and instrument metadata (expiry, strike, etc).

3. **Data Layer**  
   Shared memory with atomic writes/reads of order book and index price.

4. **Strategy Engine**  
   - Consumes real-time order book data.  
   - Detects arbitrage/hedge opportunities.  
   - Generates trade signals.  

5. **Servers & Notification**  
   - Exposes HTTP endpoints for target/close requests.  
   - Sends alerts via Telegram.  

---

## Architecture Diagram

### Mermaid (renders directly in GitLab/GitHub):

```mermaid
flowchart TD
    subgraph Deribit
        REST[Deribit REST API] -->|Auth, Instruments| Hedger
        FIX[Deribit FIX 4.4] -->|Market Data| Hedger
    end

    subgraph Hedger
        OB[Shared Order Book (HFT)]
        Strategy[Strategy Engine<br>(Box Spread, EM Calendar future)]
        HTTP[Hedge HTTP Server<br>/target /update_mm]

        FIX --> OB
        REST --> OB
        OB --> Strategy
        Strategy --> HTTP
    end

    subgraph External
        MainMarket[Main Market<br>(spot/futures)]
        Notifier[Telegram / Alerts]
    end

    HTTP --> MainMarket
    Strategy --> Notifier

---

## Features

- **Deribit Authentication**
  - Supports both REST API JWT token fetching and FIX login with nonce/timestamp authentication.

- **Order Book Management**
  - Shared memory order books with cache-line alignment for HFT performance.
  - Atomic updates and lock-free reads via `sync/atomic`.

- **Market Data (FIX)**
  - Subscribes to BTC option instruments and BTC index price via FIX 4.4.
  - Optimized parsing for incremental (X) and snapshot (W) messages.
  - O(1) symbol lookup with pre-indexed option universe.

- **Strategy Engine**
  - Current implementation: **Box Spread HFT** (risk-neutral arbitrage between strikes).
  - Infrastructure supports additional strategies (Expected Move Calendar, Collars, etc.).
  - Strategy signals are logged and optionally sent to Telegram.

- **HTTP Hedge API**
  - `/hedge/target`: Set hedge target (side, qty, base, index).
  - `/hedge/update_mm`: Push main-market unrealized PnL updates.
  - Allows coordination between hedger and main market positions.

- **Notifications**
  - Optional Telegram integration for alerts (entry, exit, close-all).

---

## Requirements

- **Go** 1.21+ (tested with Go 1.22)
- **Deribit Account**
  - API keys (client ID & secret) with trading permissions.
- **QuickFIX/Go**
  - FIX engine for Deribit connectivity.
- **Environment Variables**  
  Required:
  ```bash
  DERIBIT_CLIENT_ID=your_client_id
  DERIBIT_CLIENT_SECRET=your_client_secret
