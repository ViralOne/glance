import { writeFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { defineConfig, type Plugin } from 'vitest/config'
import { svelte } from '@sveltejs/vite-plugin-svelte'

const OUT_DIR = '../internal/web/dist'

// `go:embed all:dist` fails to compile when dist holds no files, so the empty
// directory is kept in git by a .gitkeep. emptyOutDir deletes that along with
// everything else, and the next `git add -A` then commits its removal —
// breaking `go vet` and `go build` for anyone who has not run the web build.
// This puts it back after every build so the two never disagree.
const keepGitkeep = (): Plugin => ({
  name: 'glance-keep-gitkeep',
  closeBundle() {
    writeFileSync(resolve(__dirname, OUT_DIR, '.gitkeep'), '')
  },
})

// Built assets land in the Go module so `go:embed all:dist` picks them up.
export default defineConfig({
  plugins: [svelte(), keepGitkeep()],
  build: {
    outDir: OUT_DIR,
    emptyOutDir: true,
  },
  server: {
    port: 5173,
    proxy: {
      '/api': 'http://localhost:8080',
      '/health': 'http://localhost:8080',
      '/glance.js': 'http://localhost:8080',
    },
  },
  test: {
    include: ['src/**/*.test.ts'],
  },
})
