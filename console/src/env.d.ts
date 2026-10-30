/// <reference types="vite/client" />

declare module 'vue-router' {
  interface RouteMeta {
    title?: string
    perms?: string[]
  }
}

export {}
