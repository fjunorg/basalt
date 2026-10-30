// 命令式 API 集中注册：按需导入模式下这些 API 的样式不会被
// unplugin-vue-components 自动注入（resolver 只处理模板组件），
// 统一在此导入 JS 与样式，视图从本模块导入（tree-shaking 生效）。
import { ElMessage, ElMessageBox } from 'element-plus'
import 'element-plus/es/components/message/style/css'
import 'element-plus/es/components/message-box/style/css'

export { ElMessage, ElMessageBox }
