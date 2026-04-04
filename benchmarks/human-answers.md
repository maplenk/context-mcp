A1
```
app/Http/Controllers/FiscalYearController.php
```

A2
```
app/Http/Controllers/OrderController.php
app/Http/Controllers/v2/OrderController.php
app/Http/Controllers/v3/OrderController.php
```

A3
```
/v1/merchant/{storeID}/order
/v1/merchant/{storeID}/order/update
/v1/merchant/{storeID}/order/cancel
/v3/merchant/{storeID}/order
/v2/merchant/{storeID}/order
```

A4
.. all payment files ..

B1
```
app/Order.php
app/v3/Order.php
app/Services/OrderPaymentsService.php
app/Services/InvoiceNumberService.php
app/Services/PaymentMappingService.php
App/Services/OrderSummaryBuilder.php
App/Services/OrderDetailBuilder.php

  Payment Gateway Integrations    
    
  1. Razorpay — app/razorPay.php  
  2. SnapMint — app/snapMint.php    
  3. Other integrations (partial list):
    - PayPal/Omnipay — paypal references in codebase  
    - UPI — Payment methods listed in schema
    - Third-party processors — TMC, PBMC, Beepkart, Uniommerce  
    
  Background Jobs for Payment Processing    
    
  Key async operations in app/Jobs/:   
  - CaptureRazorpayPaymentsJob — Capture pending payments  
  - CreateRazorpayInvoice — Generate Razorpay invoices
  - SendInvoiceJob — Email invoices to customers 
  - ProcessAutoInvoiceConfigJob — Automated invoice generation for scheduled orders 
  - EcomVoidOrderPaymentRefundJob, ExpireOrderPaymentRefundJob — Refund handling    
  - verifyVendorPaymentJob, TMCPaymentJob, PBMChallanPaymentJob — Vendor payments   
  - reSyncMunicipalPaymentJob — Municipal integration syncing   
    
  Billing & Invoice Processing    
    
  Views (for invoices): 
  - resources/views/invoices/sales-order.blade.php — Sales order invoices 
  - resources/views/invoices/order.blade.php — Order invoices   
    
  Related Services:
  - app/Services/OrderAccountingService.php — Accounting entry generation 
    
  Controllers & API Endpoints
    
 app/Http/Controllers/:    
  - BillingWeb.php — Web billing operations 
  - OrderController.php — Order & payment endpoints   
  - v3/OrderController.php — Order & payment endpoints
```

B2
```
app/Http/Middleware/OauthMiddleware.php 
app/Http/Middleware/webMiddlewareV2.php
config/session.php
app/Http/Middleware/thirdPartyToken.php 
app/Http/Middleware/partnerAppToken.php
app/Http/Middleware/EcomMiddleware.php 
POST /v1/merchant/{chainID}/login
POST /v2/user/loginCheck
POST /oauth/token
```

B3
```
  - Core logic: app/Order.php — manageLoyaltyPoint()
  - Config: crmSettings table
  - Ledger: loyaltyPointsLedger table tracks EARNED/REDEEMED/EXPIRED transactions
  - Customer fields: pointsCollected, pointsRedeemed, availablePoints
  - Jobs: loyaltyPointAdjustmentJob, VoidLoyaltyPointJob, loyaltyPointExpiryJob
  - Crons: checkLoyaltyPointExpiry, SendPointsExpiryMessage, SendBirthdayMessage

  ThirdParty
  app/MQLoyal.php 
  app/EasyRewardz.php
  app/Mobiquest.php 
```

B4
```
database/migrations/*
Schema update: app/Console/Commands/UpdateSchema.php
Admin.php::createSchemaRoutes ...
cloud_schema/*.sql
    chain.sql
    store.sql
```

B5
```
app/Exceptions/Handler.php
app('sentry')->captureException($e)  
Log facade
```

B6
```
app/Events/OrderCreated.php
app/Listeners/EasyEcomSaleOrderListener.php
app/Listeners/UnicommerceListener.php
v1/*/updateOnlineOrderStatusQB
app/EasyEcom.php
app/Unicommerce.php
app/OnlineOrder.php
app/Jobs/Unicommerce*.php
app/Jobs/EasyEcom*.php
app/Controllers/OrderController.php
```

C1
```
/v1/merchant/{storeID}/order
/v3/merchant/{storeID}/order
/v2/merchant/{storeID}/order
app/Http/Controllers/OrderController.php
app/Http/Controllers/v2/OrderController.php
app/Http/Controllers/v3/OrderController.php
app/Order.php
app/v3/Order.php
app/Services/OrderPaymentsService.php
app/Services/InvoiceNumberService.php
app/Services/PaymentMappingService.php
App/Services/OrderSummaryBuilder.php
App/Services/OrderDetailBuilder.php
app/Services/JobDispatcher.php
....
```

C2
```
POST /v1/merchant/{chainID}/stockTransaction 
POST /v2/merchant/{chainID}/stockTransactionWeb
POST /v1/merchant/{chainID}/bulkStockTransactionWeb
POST /v2/merchant/{chainID}/bulkStockTransactionWeb
Inventory.php:stockTransaction()
StockLedger.php
InventoryController.php
InventoryWeb.php
```

C3
```
app/Http/routes.webhooks.php
app/Http/routes.php
app/Http/routesWeb.v2.php
v1/zomato/receiveOnlineOrder
v2/gupshup/callback/events 
v2/unicommerce/saleOrderCallback 
v1/zoho/receivePO
app/Jobs/dispatchWebHookJob.php
app/Webhook.php
app/WebhookTestModel.php
app/Http/Controllers/WebhookController.php
```

C4
```
app/Http/Middleware/OpenTelemetryMiddleware.php
app/OpenTelemetry/OpenTelemetryTracer.php
app/OpenTelemetry/OpenTelemetrySpan.php
app/OpenTelemetry/OpenTelemetryFactory.php
app/OpenTelemetry/ClickHouseExporter.php
app/OpenTelemetry/ClickHouseTransport.php
app/Services/InstrumentedClickHouseService.php
config/opentelemetry.php
```

C5
```
POST /v1/merchant/{chainID}/stockTransaction 
POST /v2/merchant/{chainID}/stockTransactionWeb
POST /v1/merchant/{chainID}/bulkStockTransactionWeb
POST /v2/merchant/{chainID}/bulkStockTransactionWeb
Inventory.php:stockTransaction()
StockLedger.php
InventoryController.php
InventoryWeb.php
```