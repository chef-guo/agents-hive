import { defineConfig, loadEnv } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import { resolve } from 'path'

/** WebSocket/HTTP 代理在连接对端关闭、后端重启时常见的可忽略错误 */
function isBenignProxyError(err: unknown): boolean {
  const e = err as NodeJS.ErrnoException & { message?: string }
  const code = e?.code
  if (code === 'EPIPE' || code === 'ECONNRESET' || code === 'ECONNREFUSED') {
    return true
  }
  const msg = typeof e?.message === 'string' ? e.message : ''
  return /write EPIPE|EPIPE|ECONNRESET|ECONNREFUSED/i.test(msg)
}

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, __dirname, '')
  const devApiTarget =
    env.VITE_DEV_API_TARGET || process.env.VITE_DEV_API_TARGET || 'http://localhost:18080'

  return {
    plugins: [react(), tailwindcss()],
    resolve: {
      alias: {
        '@': resolve(__dirname, 'src'),
      },
    },
    server: {
      port: 3000,
      proxy: {
        '/api': {
          target: devApiTarget,
          changeOrigin: true,
          ws: true, // 启用 WebSocket 代理
          // 降低 http-proxy 自身日志；Vite 仍会打部分 ws 行，下面 configure 尽量吞掉良性错误
          logLevel: 'silent',
          configure: (proxy) => {
            // 忽略 WebSocket/HTTP 代理在连接断开、后端重启时的良性错误
            proxy.on('error', (err, _req, res) => {
              if (isBenignProxyError(err)) {
                return
              }
              console.error('[proxy error]', (err as Error).message)
              if (res && 'writeHead' in res && !res.headersSent) {
                ;(res as import('http').ServerResponse).writeHead(502, { 'Content-Type': 'text/plain' })
                ;(res as import('http').ServerResponse).end('Bad Gateway')
              }
            })
            proxy.on('proxyReqWs', (_proxyReq, _req, socket) => {
              socket.on('error', (err) => {
                if (isBenignProxyError(err)) {
                  return
                }
                console.error('[ws proxy socket error]', (err as Error).message)
              })
            })
          },
        },
      },
    },
    build: {
      // 直出到 Go embed 目标目录（internal/webui/embed.go 的 //go:embed dist/* 只认相对子树）
      // 省掉旧的 `make frontend-embed` rsync 搬运步骤。
      outDir: '../internal/webui/dist',
      emptyOutDir: true,
      sourcemap: false,
      rollupOptions: {
        output: {
          manualChunks: (id) => {
            if (id.includes('node_modules')) {
              if (id.includes('react') || id.includes('react-dom') || id.includes('react-router')) {
                return 'vendor-react'
              }
              if (
                id.includes('rehype-katex') ||
                id.includes('rehype') ||
                id.includes('remark') ||
                id.includes('unified') ||
                id.includes('hast') ||
                id.includes('mdast') ||
                id.includes('micromark') ||
                id.includes('vfile')
              ) {
                return 'vendor-markdown'
              }
              if (id.includes('/node_modules/katex/')) {
                return 'vendor-katex'
              }
              if (id.includes('i18next')) {
                return 'vendor-i18n'
              }
              if (id.includes('lucide-react') || id.includes('zustand')) {
                return 'vendor-ui'
              }
            }
          },
        },
      },
    },
  }
})
