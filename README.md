# 🍔 Sipariş Uygulaması

Go (Golang) ve Fiber kullanılarak geliştirilmiş basit bir **Yemek Siparişi Uygulaması**.  
Bu uygulama gerçek bir veritabanı yerine **in-memory** yapı kullanır ve **SSE (Server-Sent Events)** ile canlı sipariş güncellemeleri sağlar.

---

## 🚀 Özellikler

- 📦 Sipariş oluşturma (`POST /orders`)
- 🔍 Sipariş görüntüleme (`GET /orders/:id`)
- 🏪 Restoranların sipariş durumunu güncellemesi (`PATCH /restaurants/:rid/orders/:id/status`)
- 🔴 Canlı sipariş akışı (SSE) (`GET /events/orders`)
- 🛠 In-memory veri tabanı ile hızlı prototip
- 🔒 Eşzamanlı erişim için **mutex** korumalı `OrderStore`
