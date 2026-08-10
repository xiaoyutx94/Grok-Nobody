// Cloudflare Email Worker for Kiro Registration
// Domain: singforge.chat
// 功能: 接收邮件 → 提取验证码 → 存入 KV → 提供 API 查询
//
// 需要绑定:
// - KV namespace: OTP_STORE
// - Email Routing: *@singforge.chat → 此 Worker
//
// 环境变量:
// - API_KEY: API 访问密钥 (在 Worker Settings 中设置)

export default {
  // 处理 HTTP 请求 (API)
  async fetch(request, env) {
    const url = new URL(request.url);

    // CORS headers
    const corsHeaders = {
      "Access-Control-Allow-Origin": "*",
      "Access-Control-Allow-Methods": "GET, DELETE, OPTIONS",
      "Access-Control-Allow-Headers": "Authorization, Content-Type, X-API-Key",
    };

    if (request.method === "OPTIONS") {
      return new Response(null, { headers: corsHeaders });
    }

    // API key authentication
    const apiKey = request.headers.get("X-API-Key") || url.searchParams.get("key");
    if (env.API_KEY && apiKey !== env.API_KEY) {
      return Response.json({ error: "unauthorized" }, { status: 401, headers: corsHeaders });
    }

    // GET /api/otp?email=xxx@singforge.chat
    if (url.pathname === "/api/otp" && request.method === "GET") {
      const email = url.searchParams.get("email");
      if (!email) {
        return Response.json({ error: "missing email param" }, { status: 400, headers: corsHeaders });
      }
      const key = email.toLowerCase();
      const data = await env.OTP_STORE.get(key, "json");
      if (!data) {
        return Response.json({ found: false, email: key }, { headers: corsHeaders });
      }
      return Response.json({
        found: true,
        email: key,
        code: data.code,
        body: data.body || "",
        timestamp: data.timestamp,
      }, { headers: corsHeaders });
    }

    // DELETE /api/otp?email=xxx (cleanup after use)
    if (url.pathname === "/api/otp" && request.method === "DELETE") {
      const email = url.searchParams.get("email");
      if (email) {
        await env.OTP_STORE.delete(email.toLowerCase());
      }
      return Response.json({ ok: true }, { headers: corsHeaders });
    }

    // GET /api/health
    if (url.pathname === "/api/health") {
      return Response.json({ status: "ok", service: "kiro-email-worker" }, { headers: corsHeaders });
    }

    return Response.json({ error: "not found" }, { status: 404, headers: corsHeaders });
  },

  // 处理收到的邮件
  async email(message, env, ctx) {
    const to = message.to.toLowerCase();
    const from = message.from.toLowerCase();
    const subject = message.headers.get("subject") || "";

    console.log(`[Email] from=${from} to=${to} subject=${subject}`);

    try {
      const rawBody = await new Response(message.raw).text();
      const strippedBody = stripHtml(rawBody);
      const fullText = subject + " " + strippedBody;

      const code = extractCode(fullText);

      if (code) {
        console.log(`[Email] OTP found: ${code} for ${to}`);

        const bodyPreview = strippedBody.substring(0, 2000);

        await env.OTP_STORE.put(to, JSON.stringify({
          code: code,
          body: bodyPreview,
          from: from,
          subject: subject,
          timestamp: Date.now(),
        }), { expirationTtl: 600 });

        if (env.CALLBACK_URL) {
          const cbBody = JSON.stringify({
            email: to,
            code: code,
            body: bodyPreview,
            key: env.API_KEY || "",
          });
          ctx.waitUntil(
            fetch(env.CALLBACK_URL, {
              method: "POST",
              headers: { "Content-Type": "application/json" },
              body: cbBody,
            }).then(r => console.log(`[Email] Callback ${r.status} for ${to}`))
              .catch(e => console.error(`[Email] Callback failed: ${e.message}`))
          );
        }
      } else {
        console.log(`[Email] No OTP code found in email to ${to}`);
        await env.OTP_STORE.put(to, JSON.stringify({
          code: null,
          body: strippedBody.substring(0, 2000),
          from: from,
          subject: subject,
          body_preview: strippedBody.substring(0, 500),
          timestamp: Date.now(),
        }), { expirationTtl: 600 });

        // Even without a code, push the body so Go backend can extract with custom extractor
        if (env.CALLBACK_URL) {
          const cbBody = JSON.stringify({
            email: to,
            code: "",
            body: strippedBody.substring(0, 2000),
            key: env.API_KEY || "",
          });
          ctx.waitUntil(
            fetch(env.CALLBACK_URL, {
              method: "POST",
              headers: { "Content-Type": "application/json" },
              body: cbBody,
            }).then(r => console.log(`[Email] Callback (no-code) ${r.status} for ${to}`))
              .catch(e => console.error(`[Email] Callback (no-code) failed: ${e.message}`))
          );
        }
      }
    } catch (err) {
      console.error(`[Email] Error processing email: ${err.message}`);
    }
  },
};

function stripHtml(html) {
  return html
    .replace(/<style[^>]*>[\s\S]*?<\/style>/gi, " ")
    .replace(/<script[^>]*>[\s\S]*?<\/script>/gi, " ")
    .replace(/<[^>]+>/g, " ")
    .replace(/&nbsp;/g, " ")
    .replace(/&amp;/g, "&")
    .replace(/&lt;/g, "<")
    .replace(/&gt;/g, ">")
    .replace(/&#39;/g, "'")
    .replace(/&quot;/g, '"')
    .replace(/\s+/g, " ")
    .trim();
}

function extractCode(text) {
  // 1. Grok-style: 3 alphanumeric chars + dash + 3 alphanumeric chars (e.g. VOL-USX, 5KO-YB4)
  const grokMatch = text.match(/\b([A-Z0-9]{3}-[A-Z0-9]{3})\b/i);
  if (grokMatch) {
    return grokMatch[1].toUpperCase();
  }

  // 2. Standard 6-digit verification codes
  const patterns = [
    /(?:verification code|security code|one[- ]?time password|code|验证码|验证代码|校验码)[：:\s]+(\d{6})/i,
    /(\d{6})\s*(?:is your|为您的|是您的)/,
    /\b(\d{6})\b/,
  ];

  const skipCodes = new Set([
    "000000", "111111", "123456", "222222", "333333",
    "444444", "555555", "666666", "777777", "888888", "999999",
  ]);

  for (const pattern of patterns) {
    const match = text.match(pattern);
    if (match && match[1] && !skipCodes.has(match[1])) {
      return match[1];
    }
  }
  return null;
}
