import type {ReactNode} from 'react'
import clsx from 'clsx'
import Link from '@docusaurus/Link'
import Layout from '@theme/Layout'
import useDocusaurusContext from '@docusaurus/useDocusaurusContext'

import styles from './index.module.css'

/** 首屏要点：每条都对应一个真实能力，不写形容词。 */
const HIGHLIGHTS = [
  {
    title: '单二进制控制面',
    body: 'Go 编译产物内嵌 React 管理台，API 与管理台同端口，无需额外 Web 服务或前端部署。',
  },
  {
    title: '插件只连本机 Agent',
    body: '业务插件依赖 agent-api，不直连控制面；中控不可用时按本地快照降级。',
  },
  {
    title: '区服治理进后台',
    body: 'namespace / BC 集群 / 大区 / 小区 / 默认入口 / 排空全部 Web 可管，不再靠配置文件硬维护。',
  },
  {
    title: '身份绑定防串区',
    body: 'Agent 首启生成身份标识，后台确认后才可调度，避免误改 serverId 导致区数据隔离出错。',
  },
  {
    title: '交付可灰度可回滚',
    body: '变更单 + 分批灰度 + 热重载 / 重启生效，支持整单回滚与目标级子集回滚。',
  },
  {
    title: '高危操作须审批',
    body: '机器（API 密钥 / MCP）只能申请，人类批准后由持久 worker 执行并留回执。',
  },
]

/** 首屏截图（文件名与 static/img 下一致）。 */
const SHOTS: {src: string; alt: string; caption: string}[] = [
  {src: 'img/dashboard.png', alt: '运维总览', caption: '运维总览：健康 KPI、服务器状态墙与连接流'},
  {src: 'img/topology.png', alt: '集群拓扑', caption: '集群拓扑：代理 → 小区放射链路与异常边'},
  {src: 'img/delivery.png', alt: '交付变更单', caption: '交付变更单：分批灰度、目标级回滚与交付历史'},
  {src: 'img/servers.png', alt: '服务器资产', caption: '服务器资产：注册待确认、身份与健康运维'},
]

function Hero(): ReactNode {
  const {siteConfig} = useDocusaurusContext()
  return (
    <header className={clsx('hero hero--primary', styles.hero)}>
      <div className="container">
        <h1 className={styles.heroTitle}>{siteConfig.title}</h1>
        <p className={styles.heroTagline}>{siteConfig.tagline}</p>
        <p className={styles.heroLead}>
          给同时运维多个 BungeeCord 代理与上百台 Bukkit / Paper 子服的服主与运维团队用。
        </p>
        <p className={styles.heroCallout}>
          <strong>控制面挂 ≠ 数据面挂</strong>：Agent 持本地快照 fail-static，
          控制面不可用时按快照继续跑，不阻断玩家进服。
        </p>
        <div className={styles.heroButtons}>
          <Link className="button button--secondary button--lg" to="/docs/wiki/quick-start">
            快速开始
          </Link>
          <Link className="button button--outline button--secondary button--lg" to="/docs/wiki/">
            浏览文档
          </Link>
        </div>
      </div>
    </header>
  )
}

function Highlights(): ReactNode {
  return (
    <section className={styles.section}>
      <div className="container">
        <h2 className={styles.sectionTitle}>它能做什么</h2>
        <div className={styles.grid}>
          {HIGHLIGHTS.map((item) => (
            <article className={styles.card} key={item.title}>
              <h3 className={styles.cardTitle}>{item.title}</h3>
              <p className={styles.cardBody}>{item.body}</p>
            </article>
          ))}
        </div>
      </div>
    </section>
  )
}

function Screenshots(): ReactNode {
  return (
    <section className={clsx(styles.section, styles.sectionAlt)}>
      <div className="container">
        <h2 className={styles.sectionTitle}>界面预览</h2>
        <p className={styles.sectionNote}>以下为演示模式截图，数据为示例数据。</p>
        <div className={styles.shotGrid}>
          {SHOTS.map((shot) => (
            <figure className={styles.shot} key={shot.src}>
              <img src={shot.src} alt={shot.alt} loading="lazy" />
              <figcaption>{shot.caption}</figcaption>
            </figure>
          ))}
        </div>
      </div>
    </section>
  )
}

export default function Home(): ReactNode {
  return (
    <Layout
      title="文档"
      description="Beacon：面向 Minecraft 多群组服务器的集群调度中间件控制面">
      <Hero />
      <main>
        <Highlights />
        <Screenshots />
      </main>
    </Layout>
  )
}
