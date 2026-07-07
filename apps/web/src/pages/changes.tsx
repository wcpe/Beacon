// 变更单页（/changes）：占位骨架，由对应页面 agent 以真实内容替换本文件
import PageScaffold from './page-scaffold'

export default function ChangesPage() {
  return <PageScaffold titleKey="nav.changes" missionKey="delivery.changes.mission" />
}
