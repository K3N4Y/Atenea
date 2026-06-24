import { globalIgnores } from 'eslint/config'
import pluginVue from 'eslint-plugin-vue'
import {
  defineConfigWithVueTs,
  vueTsConfigs,
} from '@vue/eslint-config-typescript'
import skipFormatting from '@vue/eslint-config-prettier/skip-formatting'

// Configuracion de ESLint (flat config) para el frontend Vue 3 + TypeScript.
// Es la cadena oficial de `create-vue`: reglas esenciales de Vue mas las
// recomendadas de TypeScript. `skipFormatting` desactiva cualquier regla de
// estilo que choque con Prettier; el reparto es claro: Prettier formatea,
// ESLint solo busca errores de codigo.
export default defineConfigWithVueTs(
  {
    name: 'app/files-to-lint',
    files: ['**/*.{ts,mts,tsx,vue}'],
  },
  // wailsjs lo genera Wails y dist es la build: no se lintan.
  globalIgnores(['**/dist/**', '**/wailsjs/**', '**/node_modules/**']),
  pluginVue.configs['flat/essential'],
  vueTsConfigs.recommended,
  skipFormatting,
)
