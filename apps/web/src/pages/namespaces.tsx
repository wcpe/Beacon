// namespace页（/namespaces）：占位骨架，由对应页面 agent 以真实内容替换本文件
import PageScaffold from './page-scaffold'

export default function NamespacesPage() {
  return <PageScaffold titleKey="nav.namespaces" missionKey="system.namespaces.mission" />
}
