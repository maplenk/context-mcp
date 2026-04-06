# Human Evaluation: context-mcp vs C-reference vs Human Answers

**Date:** 2026-04-03
**Repo:** qbapi (18,655 nodes, 24,882 edges in context-mcp; 33,576 nodes, 77,157 edges in C-ref)
**context-mcp version:** commit 4b13987 (post DA-round-14 fixes)
**C-reference version:** codebase-memory-mcp v0.10.0
**Raw output artifacts:**
- `benchmarks/results/v0.10.0-4b13987-qbapi-raw.txt` -- core 6 queries (A1, A3, B1, B6, C1, C5)
- `benchmarks/results/v0.10.0-4b13987-qbapi-extra-raw.txt` -- remaining 9 queries (A2, A4, B2-B5, C2-C4)
**Answer key:** `benchmarks/human-answers.md` (grading is against the FULL answer key, not subsets)

## Scoring Methodology

Each query in `benchmarks/human-answers.md` lists specific files, classes, methods, endpoints, and jobs as the expected answer. Every distinct item in the answer key is counted as one rubric item. A result is a HIT if the exact file or a method/class within that file appears in the context-mcp or C-ref results. API endpoints (e.g. `POST /v1/merchant/{storeID}/order`) are counted as HITs if a result references the controller that serves them.

## Scorecard

| Query | Category | Rubric Items | context-mcp | C-ref | Winner |
|-------|----------|-------------|------------|-------|--------|
| A1 | Exact | 1 | **1/1** | -- | **qb** |
| A2 | Exact | 3 | 1/3 | -- | -- |
| A3 | Regex | 5 | 3/5 | -- | -- |
| A4 | Regex | (broad) | partial | -- | -- |
| B1 | Concept | 20 | **4/20** | **3/20** | qb (marginal) |
| B2 | Concept | 9 | **0/9** | **1/9** | C-ref |
| B3 | Concept | 9 | **2/9** | -- | -- |
| B4 | Concept | 5 | **1/5** | -- | -- |
| B5 | Concept | 3 | **0/3** | -- | -- |
| B6 | Concept | 10 | **0/10** | **0/10** | Tie (both fail) |
| C1 | Cross-file | 11 | **0/11** | **2/11** | C-ref |
| C2 | Cross-file | 8 | **1/8** | **5/8** | C-ref |
| C3 | Cross-file | 11 | **0/11** | **4/11** | C-ref |
| C4 | Cross-file | 8 | **5/8** | **3/8** | **context-mcp** |
| C5 | Cross-file | 8 | **0/8** | **5/8** | C-ref |

### Aggregate Hit Rates

| Category | context-mcp | Denominator | Rate |
|----------|-----------|-------------|------|
| Concept (B1-B6) | 7 | 56 | **12.5%** |
| Cross-file (C1-C5) | 6 | 46 | **13.0%** |
| **B+C combined** | **13** | **102** | **12.7%** |

| Category | C-ref (tested queries only) | Denominator | Rate |
|----------|-----------|-------------|------|
| Concept (B1,B2,B6) | 4 | 39 | 10.3% |
| Cross-file (C1-C5) | 19 | 46 | **41.3%** |
| **B+C combined** | **23** | **85** | **27.1%** |

---

## Detailed Results Per Query

### A1 -- Exact: FiscalYearController

**Full rubric (1 item):** `app/Http/Controllers/FiscalYearController.php`

| Tool | Result | Grade |
|------|--------|-------|
| context-mcp `read_symbol` | Exact match, 581us | **1/1** |

### A2 -- Exact: OrderController

**Full rubric (3 items):** OrderController.php, v2/OrderController.php, v3/OrderController.php

| Tool | Result | Grade |
|------|--------|-------|
| context-mcp `read_symbol` | Returns only v1 `app/Http/Controllers/OrderController.php` | **1/3** |

### A3 -- Regex: POST.*order

**Full rubric (5 items):** /v1/.../order, /v1/.../order/update, /v1/.../order/cancel, /v3/.../order, /v2/.../order

| Tool | Result | Grade |
|------|--------|-------|
| context-mcp `search_code` | Found /v3 order endpoint, OrderController, v2/OrderController, OrderDeletionController. Missing /v1/order/update, /v1/order/cancel | **3/5** |

### A4 -- Regex: payment|razorpay|billing

**Full rubric:** "all payment files" (broad, not graded numerically)

| Tool | Result | Grade |
|------|--------|-------|
| context-mcp `search_code` | 15 matches in AUMMunicipal, Account.php -- only `payment` hits, no razorpay/billing files | **partial** |

### B1 -- Concept: Payment processing and billing logic

**Full rubric (20 items):**
1. app/Order.php
2. app/v3/Order.php
3. app/Services/OrderPaymentsService.php
4. app/Services/InvoiceNumberService.php
5. app/Services/PaymentMappingService.php
6. app/Services/OrderSummaryBuilder.php
7. app/Services/OrderDetailBuilder.php
8. app/razorPay.php
9. app/snapMint.php
10. app/Jobs/CaptureRazorpayPaymentsJob
11. app/Jobs/CreateRazorpayInvoice
12. app/Jobs/SendInvoiceJob
13. app/Jobs/ProcessAutoInvoiceConfigJob
14. app/Jobs/EcomVoidOrderPaymentRefundJob + ExpireOrderPaymentRefundJob
15. app/Jobs/verifyVendorPaymentJob + TMCPaymentJob + PBMChallanPaymentJob
16. app/Jobs/reSyncMunicipalPaymentJob
17. resources/views/invoices/
18. app/Services/OrderAccountingService.php
19. app/Http/Controllers/BillingWeb.php
20. app/Http/Controllers/v3/OrderController.php

| Item | context-mcp | C-ref |
|------|------------|-------|
| OrderPaymentsService | HIT (#1,5,6) | MISS |
| BillingWeb | HIT (#2-4) | HIT (#1,3-6) |
| OrderSummaryBuilder | HIT (#7) | MISS |
| PaymentMappingService | MISS | HIT (#9) |
| Order.php | MISS | MISS |
| v3/Order.php | MISS | MISS |
| InvoiceNumberService | MISS | MISS |
| OrderDetailBuilder | MISS | MISS |
| razorPay.php | MISS | MISS |
| snapMint.php | MISS | MISS |
| All Jobs | MISS | MISS |
| OrderAccountingService | MISS | MISS |
| v3/OrderController | MISS | MISS |
| invoices views | MISS | MISS |
| **Grade** | **4/20** | **3/20** |

### B2 -- Concept: Authentication and session management

**Full rubric (9 items):**
1. app/Http/Middleware/OauthMiddleware.php
2. app/Http/Middleware/webMiddlewareV2.php
3. config/session.php
4. app/Http/Middleware/thirdPartyToken.php
5. app/Http/Middleware/partnerAppToken.php
6. app/Http/Middleware/EcomMiddleware.php
7. POST /v1/merchant/{chainID}/login
8. POST /v2/user/loginCheck
9. POST /oauth/token

| Item | context-mcp | C-ref |
|------|------------|-------|
| OauthMiddleware | MISS | MISS |
| webMiddlewareV2 | MISS | MISS |
| config/session.php | MISS | MISS |
| thirdPartyToken | MISS | MISS |
| partnerAppToken | MISS | MISS |
| EcomMiddleware | MISS | HIT (#2,5) |
| login endpoint | MISS | MISS |
| loginCheck endpoint | MISS | MISS |
| oauth/token endpoint | MISS | MISS |
| Authenticate.php (not in rubric) | found (#1) | MISS |
| **Grade** | **0/9** | **1/9** |

Note: context-mcp found Authenticate.php (#1) which is related but NOT in the answer key. Not counted.

### B3 -- Concept: Loyalty points and rewards

**Full rubric (9 items):**
1. app/Order.php -- manageLoyaltyPoint()
2. crmSettings table
3. loyaltyPointsLedger table
4. loyaltyPointAdjustmentJob
5. VoidLoyaltyPointJob
6. loyaltyPointExpiryJob
7. checkLoyaltyPointExpiry + SendPointsExpiryMessage + SendBirthdayMessage crons
8. app/MQLoyal.php
9. app/EasyRewardz.php + app/Mobiquest.php

| Item | context-mcp |
|------|------------|
| Order.php manageLoyaltyPoint | MISS |
| crmSettings table | MISS |
| loyaltyPointsLedger | MISS |
| loyaltyPointAdjustmentJob | HIT (#9) |
| VoidLoyaltyPointJob | MISS |
| loyaltyPointExpiryJob | MISS |
| Crons | MISS |
| MQLoyal | MISS |
| EasyRewardz | HIT (#7) |
| Mobiquest | MISS |
| **Grade** | **2/9** |

### B4 -- Concept: Database schema and migrations

**Full rubric (5 items):**
1. database/migrations/*
2. app/Console/Commands/UpdateSchema.php
3. Admin.php::createSchemaRoutes
4. cloud_schema/chain.sql
5. cloud_schema/store.sql

| Item | context-mcp |
|------|------------|
| database/migrations/* | HIT (#2-9) |
| UpdateSchema.php | MISS |
| Admin createSchemaRoutes | MISS |
| cloud_schema/chain.sql | MISS |
| cloud_schema/store.sql | MISS |
| **Grade** | **1/5** |

### B5 -- Concept: Error handling, logging, monitoring

**Full rubric (3 items):**
1. app/Exceptions/Handler.php
2. Sentry integration (app('sentry')->captureException)
3. Log facade

| Item | context-mcp |
|------|------------|
| Exceptions/Handler.php | MISS |
| Sentry integration | MISS |
| Log facade | MISS |
| **Grade** | **0/3** |

### B6 -- Concept: Omnichannel integration sync

**Full rubric (10 items):**
1. app/Events/OrderCreated.php
2. app/Listeners/EasyEcomSaleOrderListener.php
3. app/Listeners/UnicommerceListener.php
4. v1/*/updateOnlineOrderStatusQB endpoint
5. app/EasyEcom.php
6. app/Unicommerce.php
7. app/OnlineOrder.php
8. app/Jobs/Unicommerce*.php
9. app/Jobs/EasyEcom*.php
10. app/Controllers/OrderController.php

| Item | context-mcp | C-ref |
|------|------------|-------|
| OrderCreated event | MISS | MISS |
| EasyEcomSaleOrderListener | MISS | MISS |
| UnicommerceListener | MISS | MISS |
| updateOnlineOrderStatusQB | MISS | MISS |
| EasyEcom.php | MISS | MISS |
| Unicommerce.php | MISS | MISS |
| OnlineOrder.php | MISS | MISS |
| Unicommerce Jobs | MISS | MISS |
| EasyEcom Jobs | MISS | MISS |
| OrderController.php | MISS | MISS |
| **Grade** | **0/10** | **0/10** |

### C1 -- Cross-file: Order creation end-to-end flow

**Full rubric (11 items):**
1. /v1/merchant/{storeID}/order endpoint
2. /v3/merchant/{storeID}/order endpoint
3. /v2/merchant/{storeID}/order endpoint
4. app/Http/Controllers/OrderController.php
5. app/Http/Controllers/v2/OrderController.php
6. app/Http/Controllers/v3/OrderController.php
7. app/Order.php
8. app/v3/Order.php
9. app/Services/OrderPaymentsService.php
10. app/Services/InvoiceNumberService.php + PaymentMappingService + OrderSummaryBuilder + OrderDetailBuilder
11. app/Services/JobDispatcher.php

| Item | context-mcp | C-ref |
|------|------------|-------|
| v1 order endpoint | MISS | MISS |
| v2 order endpoint | MISS | MISS |
| v3 order endpoint | MISS | MISS |
| OrderController (v1) | MISS | MISS |
| OrderController (v2) | MISS | MISS |
| OrderController (v3) | MISS | MISS |
| Order.php | MISS | HIT (#4 orderCheck, #5-6 Order.php) |
| v3/Order.php | MISS | MISS |
| OrderPaymentsService | MISS | MISS |
| Services (Invoice/Payment/Summary/Detail) | MISS | MISS |
| JobDispatcher | MISS | MISS |
| v3/OrderProcessingContext (bonus) | MISS | HIT (#1 orderData, #7 orderDataValue) |
| **Grade** | **0/11** | **2/11** |

**context-mcp failure:** Top result is test code (V3OrderApiTest). Ranks 4-9 are openspout `.end()` methods. Ranks 10-15 are Flight.endTrip, endLeg, endSession -- all noise from "end to end" in query.

### C2 -- Cross-file: Stock transaction API to database

**Full rubric (8 items):**
1. POST /v1/merchant/{chainID}/stockTransaction endpoint
2. POST /v2/merchant/{chainID}/stockTransactionWeb endpoint
3. POST /v1/merchant/{chainID}/bulkStockTransactionWeb endpoint
4. POST /v2/merchant/{chainID}/bulkStockTransactionWeb endpoint
5. Inventory.php:stockTransaction()
6. StockLedger.php
7. InventoryController.php
8. InventoryWeb.php

| Item | context-mcp | C-ref |
|------|------------|-------|
| stockTransaction endpoint (v1) | MISS | MISS |
| stockTransactionWeb endpoint (v2) | MISS | MISS |
| bulkStockTransactionWeb (v1) | MISS | MISS |
| bulkStockTransactionWeb (v2) | MISS | MISS |
| Inventory.php stockTransaction | MISS | HIT (#4 stockTransactionCheck, #6 stockTransaction) |
| StockLedger.php | HIT (#1 StockLedger.stockTransaction) | HIT (#3) |
| InventoryController.php | MISS | HIT (#3-4) |
| InventoryWeb.php | MISS | HIT (#5-6) |
| **Grade** | **1/8** | **5/8** |

### C3 -- Cross-file: Webhook handling

**Full rubric (11 items):**
1. app/Http/routes.webhooks.php
2. app/Http/routes.php
3. app/Http/routesWeb.v2.php
4. v1/zomato/receiveOnlineOrder endpoint
5. v2/gupshup/callback/events endpoint
6. v2/unicommerce/saleOrderCallback endpoint
7. v1/zoho/receivePO endpoint
8. app/Jobs/dispatchWebHookJob.php
9. app/Webhook.php
10. app/WebhookTestModel.php
11. app/Http/Controllers/WebhookController.php

| Item | context-mcp | C-ref |
|------|------------|-------|
| routes.webhooks.php | MISS | MISS |
| routes.php | MISS | MISS |
| routesWeb.v2.php | MISS | MISS |
| zomato/receiveOnlineOrder | MISS | MISS |
| gupshup/callback/events | MISS | MISS |
| unicommerce/saleOrderCallback | MISS | MISS |
| zoho/receivePO | MISS | MISS |
| dispatchWebHookJob | MISS | MISS |
| Webhook.php | MISS | HIT (#1) |
| WebhookTestModel.php | MISS | HIT (#2) |
| WebhookController.php | MISS | HIT (#4, #6 v2) |
| Scripts.WebhookbyOrderID (bonus) | HIT (#10) -- not in rubric | HIT (#8) |
| **Grade** | **0/11** | **4/11** |

**context-mcp failure:** All top 10 results are middleware `.handle()` methods. The word "handling" in the query matched every middleware's handle method.

### C4 -- Cross-file: OpenTelemetry tracing

**Full rubric (8 items):**
1. app/Http/Middleware/OpenTelemetryMiddleware.php
2. app/OpenTelemetry/OpenTelemetryTracer.php
3. app/OpenTelemetry/OpenTelemetrySpan.php
4. app/OpenTelemetry/OpenTelemetryFactory.php
5. app/OpenTelemetry/ClickHouseExporter.php
6. app/OpenTelemetry/ClickHouseTransport.php
7. app/Services/InstrumentedClickHouseService.php
8. config/opentelemetry.php

| Item | context-mcp | C-ref |
|------|------------|-------|
| OpenTelemetryMiddleware | HIT (#2) | MISS |
| OpenTelemetryTracer | HIT (#1, #7, #13) | HIT (#6) |
| OpenTelemetrySpan | HIT (#6, #9) | HIT (#4) |
| OpenTelemetryFactory | HIT (#2 file, #11) | HIT (#3) |
| ClickHouseExporter | HIT (#8) | MISS |
| ClickHouseTransport | MISS | MISS |
| InstrumentedClickHouseService | MISS | MISS |
| config/opentelemetry.php | MISS | MISS |
| **Grade** | **5/8** | **3/8** |

### C5 -- Cross-file: Inventory API to database write flow

**Full rubric (8 items):**
1. POST /v1/merchant/{chainID}/stockTransaction endpoint
2. POST /v2/merchant/{chainID}/stockTransactionWeb endpoint
3. POST /v1/merchant/{chainID}/bulkStockTransactionWeb endpoint
4. POST /v2/merchant/{chainID}/bulkStockTransactionWeb endpoint
5. Inventory.php:stockTransaction()
6. StockLedger.php
7. InventoryController.php
8. InventoryWeb.php

| Item | context-mcp | C-ref |
|------|------------|-------|
| stockTransaction endpoint (v1) | MISS | MISS |
| stockTransactionWeb endpoint (v2) | MISS | MISS |
| bulkStockTransactionWeb (v1) | MISS | MISS |
| bulkStockTransactionWeb (v2) | MISS | MISS |
| Inventory.php stockTransaction | MISS (inventory class at #13, wrong method) | HIT (#1-2) |
| StockLedger.php | MISS | MISS |
| InventoryController.php | MISS | HIT (#3-4) |
| InventoryWeb.php | MISS | HIT (#5-6) |
| **Grade** | **0/8** | **5/8** |

**context-mcp failure:** Top 4 hits are migration files. Test file at #6. inventory class appears at #13 but wrong method (formatAttributeMappingOptionsForResponse, not stockTransaction).

---

## Root Cause Analysis

### 1. Lexical Noise Domination
FTS5 BM25 matches common method names in queries:
- **"end to end"** matches every `.end()` method (C1: 6 of 15 results are openspout `.end()`)
- **"handling"** matches every `.handle()` middleware method (C3: 10 of 15 results are middleware)
- **"database write"** matches migration filenames (C5: 4 of top 9 are migrations)

### 2. No Path-Based Penalties
Test files (V3OrderApiTest at C1 #1, ApiMonitorMiddlewareTest at C2 #6), migration files (C5 #1-4), and vendor/lib code (openspout at C1 #4-9) rank equally with production source code.

### 3. Index Size Gap
context-mcp: 18,655 nodes / 24,882 edges. C-ref: 33,576 / 77,157. The 1.8x node gap and 3.1x edge gap mean weaker graph signals for PPR, betweenness, and in-degree.

### 4. Business Vocabulary Gap
"Omnichannel" is domain jargon mapping to EasyEcom/Unicommerce/OnlineOrder. Neither tool bridges this.

### 5. No Call-Graph Cluster Boosting
Results are scored independently. Related symbols (StockLedger.stockTransaction calling Inventory.stockTransaction) don't boost each other.

---

## Latency

| Query | context-mcp |
|-------|-----------|
| A1 exact | 581us |
| A3 regex | 111ms |
| B1 concept | 9.0ms |
| B6 concept | 1.7ms |
| C1 cross-file | 4.7ms |
| C5 cross-file | 2.9ms |

All queries complete under 120ms.
