import { defineConfig } from 'vite'
import { fileURLToPath, URL } from 'node:url'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

const externalPrefixes = ['@fontsource-variable/', 'lucide-react', 'radix-ui', 'react', 'recharts']
const externalExact = new Set(['class-variance-authority', 'clsx', 'sonner', 'tailwind-merge'])

function isExternal(id: string) {
  return (
    externalExact.has(id) ||
    externalPrefixes.some((prefix) => id === prefix || id.startsWith(`${prefix}/`))
  )
}

export default defineConfig({
  plugins: [react(), tailwindcss()],
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    lib: {
      entry: fileURLToPath(new URL('./src/index.ts', import.meta.url)),
      formats: ['es'],
      fileName: 'index',
    },
    rollupOptions: {
      external: isExternal,
    },
  },
})
