import { useMemo, useState, type ReactNode } from 'react'
import {
  Activity,
  AlertTriangle,
  ArchiveRestore,
  Boxes,
  CheckCircle2,
  CircleHelp,
  Database,
  Gauge,
  ListChecks,
  MoreHorizontal,
  Pause,
  Play,
  Search,
  Server,
  ShieldAlert,
  SlidersHorizontal,
  Terminal,
  TrendingUp,
  Users,
  Zap,
} from 'lucide-react'
import { toast } from 'sonner'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
  AnchorRailLayout,
  AnchorSectionBlock,
  AsyncSection,
  Badge,
  badgeVariants,
  Button,
  buttonVariants,
  Card,
  CardContent,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
  Checkbox,
  cn,
  Combobox,
  DataTable,
  type DataTableColumn,
  DestructiveConfirmDialog,
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
  GaugeRing,
  HealthBar,
  type HealthSegment,
  IconStat,
  Input,
  KpiCard,
  Label,
  MarkdownLite,
  MiniSparkline,
  ratioLevel,
  ScrollArea,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
  Separator,
  CardGridSkeleton,
  Sheet,
  SheetContent,
  SheetDescription,
  SheetFooter,
  SheetHeader,
  SheetTitle,
  SheetTrigger,
  Skeleton,
  SectionHeader,
  PageHeader,
  SummaryStrip,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
  TableSkeleton,
  Tabs,
  TabsContent,
  TabsList,
  tabsListVariants,
  TabsTrigger,
  Textarea,
  Toaster,
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
  TileGridSkeleton,
} from '@beacon/ui'
import { UI_WIKI_COVERED_EXPORTS } from './coverage'

interface MuseumItem {
  id: string
  title: string
  group: string
  description: string
  exports: string[]
  states: string[]
  preview: ReactNode
}

const tableColumns: DataTableColumn<{ server: string; status: string; speed: string }>[] = [
  { header: '服务器', cell: (row) => row.server },
  { header: '状态', cell: (row) => <Badge variant="secondary">{row.status}</Badge> },
  { header: '速度', cell: (row) => row.speed, className: 'font-mono text-xs' },
]

const healthSegments: HealthSegment[] = [
  { label: '在线', count: 742, level: 'ok' },
  { label: '亚健康', count: 18, level: 'warn' },
  { label: '离线', count: 5, level: 'danger' },
]

const sectionItems = [
  { id: 'base', label: '基础信息' },
  { id: 'state', label: '状态与反馈' },
  { id: 'data', label: '数据展示' },
]

function ComponentShell({ children }: { children: ReactNode }) {
  return <div className="rounded-lg border bg-card p-4">{children}</div>
}

function ExportList({ names }: { names: string[] }) {
  return (
    <div className="flex flex-wrap gap-1.5">
      {names.map((name) => (
        <Badge key={name} variant="outline" className="font-mono">
          {name}
        </Badge>
      ))}
    </div>
  )
}

function ButtonPreview() {
  return (
    <ComponentShell>
      <div className="flex flex-wrap items-center gap-2">
        <Button>默认</Button>
        <Button variant="secondary">次要</Button>
        <Button variant="outline">描边</Button>
        <Button variant="ghost">幽灵</Button>
        <Button variant="destructive">危险</Button>
        <Button variant="link">链接</Button>
        <Button size="sm">
          <Play />
          小按钮
        </Button>
        <Button size="lg">大按钮</Button>
        <Button size="icon" aria-label="暂停">
          <Pause />
        </Button>
        <Button disabled>禁用</Button>
      </div>
      {/* B 版状态药丸：浅底 + 描边 + 语义色文字 + 前导圆点 */}
      <div className="mt-3 flex flex-wrap items-center gap-2">
        <Badge variant="ok">
          <span className="size-1.5 rounded-full bg-current" />
          在线
        </Badge>
        <Badge variant="warn">
          <span className="size-1.5 rounded-full bg-current" />
          降级
        </Badge>
        <Badge variant="crit">
          <span className="size-1.5 rounded-full bg-current" />
          危急
        </Badge>
        <Badge variant="off">
          <span className="size-1.5 rounded-full bg-current" />
          离线
        </Badge>
        <Badge variant="brand">品牌</Badge>
      </div>
    </ComponentShell>
  )
}

function PageHeaderPreview() {
  return (
    <ComponentShell>
      <div className="space-y-5">
        <PageHeader
          icon={<Server className="size-4" />}
          title="服务器资产"
          description="查看健康、待确认接入与资产清单"
          actions={
            <>
              <Button variant="outline" size="sm">
                导出
              </Button>
              <Button size="sm">接入新服</Button>
            </>
          }
        />
        <PageHeader
          icon={<Terminal className="size-4" />}
          title="命令观测"
          description="在途队列与历史命令生命周期"
          actions={
            <Select defaultValue="1h">
              <SelectTrigger className="w-28">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="1h">近 1 小时</SelectItem>
                <SelectItem value="24h">近 24 小时</SelectItem>
              </SelectContent>
            </Select>
          }
        />
        <SectionHeader
          icon={<ListChecks className="size-4" />}
          title="区段标题 base"
          count="12 项"
          actions={<Button variant="ghost" size="sm">刷新</Button>}
        />
        <SectionHeader
          size="lg"
          icon={<Gauge className="size-4" />}
          title="区段标题 lg"
          count="页内主区段"
        />
      </div>
    </ComponentShell>
  )
}

function FormPreview() {
  const [checked, setChecked] = useState(true)
  const [cluster, setCluster] = useState('prod-a')
  const [region, setRegion] = useState('north')
  return (
    <ComponentShell>
      <div className="grid gap-4 md:grid-cols-2">
        <div className="space-y-2">
          <Label htmlFor="wiki-input">配置目录</Label>
          <Input id="wiki-input" placeholder="plugins/Beacon" />
        </div>
        <div className="space-y-2">
          <Label htmlFor="wiki-combobox">BungeeCord 集群</Label>
          <Combobox
            id="wiki-combobox"
            value={cluster}
            onChange={setCluster}
            options={[
              { value: 'prod-a', label: 'prod-a · 正式 A 组' },
              { value: 'prod-b', label: 'prod-b · 正式 B 组' },
              { value: 'test-a', label: 'test-a · 测试组' },
            ]}
            allowCustom={false}
            clearable
            clearLabel="清空集群"
          />
        </div>
        <div className="space-y-2">
          <Label>大区</Label>
          <Select value={region} onValueChange={setRegion}>
            <SelectTrigger>
              <SelectValue placeholder="选择大区" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="north">北部大区</SelectItem>
              <SelectItem value="east">东部大区</SelectItem>
              <SelectItem value="south">南部大区</SelectItem>
            </SelectContent>
          </Select>
        </div>
        <label className="flex items-center gap-2 self-end rounded-md border px-3 py-2 text-sm">
          <Checkbox checked={checked} onCheckedChange={(value) => { setChecked(value === true); }} />
          启用失败熔断保护
        </label>
        <div className="space-y-2 md:col-span-2">
          <Label htmlFor="wiki-textarea">发布备注</Label>
          <Textarea id="wiki-textarea" placeholder="说明本次灰度同步的变更范围" />
        </div>
      </div>
    </ComponentShell>
  )
}

function OverlayPreview() {
  const [confirmOpen, setConfirmOpen] = useState(false)
  return (
    <ComponentShell>
      <div className="flex flex-wrap items-center gap-2">
        <Dialog>
          <DialogTrigger asChild>
            <Button variant="outline">Dialog</Button>
          </DialogTrigger>
          <DialogContent>
            <DialogHeader>
              <DialogTitle>编辑批次参数</DialogTitle>
              <DialogDescription>用于展示标准表单对话框结构。</DialogDescription>
            </DialogHeader>
            <Input defaultValue="20" />
            <DialogFooter>
              <Button>保存</Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
        <Sheet>
          <SheetTrigger asChild>
            <Button variant="outline">Sheet</Button>
          </SheetTrigger>
          <SheetContent>
            <SheetHeader>
              <SheetTitle>服务器详情</SheetTitle>
              <SheetDescription>抽屉适合承载较长的运维明细。</SheetDescription>
            </SheetHeader>
            <div className="grid gap-2 px-4 text-sm">
              <Badge variant="secondary">online</Badge>
              <span className="font-mono">10.24.8.12:25565</span>
            </div>
            <SheetFooter>
              <Button variant="outline">关闭</Button>
            </SheetFooter>
          </SheetContent>
        </Sheet>
        <AlertDialog>
          <AlertDialogTrigger asChild>
            <Button variant="destructive">AlertDialog</Button>
          </AlertDialogTrigger>
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>确认终止同步？</AlertDialogTitle>
              <AlertDialogDescription>后续批次会停止下发，已完成目标保持现状。</AlertDialogDescription>
            </AlertDialogHeader>
            <AlertDialogFooter>
              <AlertDialogCancel>取消</AlertDialogCancel>
              <AlertDialogAction variant="destructive">终止</AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button variant="outline" size="icon" aria-label="更多">
              <MoreHorizontal />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent>
            <DropdownMenuLabel>批次操作</DropdownMenuLabel>
            <DropdownMenuSeparator />
            <DropdownMenuItem>暂停</DropdownMenuItem>
            <DropdownMenuItem>继续</DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
        <TooltipProvider>
          <Tooltip>
            <TooltipTrigger asChild>
              <Button variant="ghost" size="icon" aria-label="说明">
                <CircleHelp />
              </Button>
            </TooltipTrigger>
            <TooltipContent>Tooltip 用于解释低频图标按钮。</TooltipContent>
          </Tooltip>
        </TooltipProvider>
        <Button variant="outline" onClick={() => { setConfirmOpen(true); }}>
          高摩擦确认
        </Button>
        <DestructiveConfirmDialog
          open={confirmOpen}
          onOpenChange={setConfirmOpen}
          title="回滚整个批次？"
          description="目标服务器会恢复到同步前备份目录。"
          confirmLabel="确认回滚"
          confirmPhrase="rollback"
          impacts={['批次：第 3 批', '目标：20 台服务器']}
          onConfirm={() => { setConfirmOpen(false); }}
        />
      </div>
    </ComponentShell>
  )
}

function DataPreview() {
  const rows = [
    { server: 'lobby-001', status: '传输中', speed: '38 MB/s' },
    { server: 'game-042', status: '成功', speed: '0 MB/s' },
    { server: 'game-113', status: '等待', speed: '-' },
  ]
  return (
    <ComponentShell>
      <div className="space-y-4">
        <SummaryStrip
          items={[
            { label: '目标总数', value: 1024 },
            { label: '成功', value: 816, tone: 'success' },
            { label: '失败', value: 3, tone: 'danger' },
          ]}
        />
        <DataTable columns={tableColumns} rows={rows} rowKey={(row) => row.server} />
      </div>
    </ComponentShell>
  )
}

function LayoutPreview() {
  return (
    <ComponentShell>
      <Tabs defaultValue="card">
        <TabsList>
          <TabsTrigger value="card">Card</TabsTrigger>
          <TabsTrigger value="line">Line Tab</TabsTrigger>
          <TabsTrigger value="rail">Anchor</TabsTrigger>
          <TabsTrigger value="scroll">ScrollArea</TabsTrigger>
        </TabsList>
        <TabsContent value="card" className="pt-3">
          <Card>
            <CardHeader>
              <CardTitle>同步任务</CardTitle>
              <CardDescription>Card 用于有明确边界的对象。</CardDescription>
            </CardHeader>
            <CardContent className="text-sm text-muted-foreground">
              当前批次 3 / 12，熔断阈值 10%。
            </CardContent>
            <CardFooter>
              <Button size="sm">查看详情</Button>
            </CardFooter>
          </Card>
        </TabsContent>
        <TabsContent value="line" className="pt-3">
          <Tabs defaultValue="a">
            <TabsList variant="line">
              <TabsTrigger value="a">清单</TabsTrigger>
              <TabsTrigger value="b">扫描</TabsTrigger>
              <TabsTrigger value="c">比对</TabsTrigger>
            </TabsList>
            <TabsContent value="a" className="pt-3 text-sm text-muted-foreground">
              下划线变体适合内容区二级导航。
            </TabsContent>
            <TabsContent value="b" className="pt-3 text-sm text-muted-foreground">
              扫描结果区。
            </TabsContent>
            <TabsContent value="c" className="pt-3 text-sm text-muted-foreground">
              跨服比对区。
            </TabsContent>
          </Tabs>
        </TabsContent>
        <TabsContent value="rail" className="h-64 pt-3">
          <AnchorRailLayout sections={sectionItems} ariaLabel="控件分区">
            <AnchorSectionBlock id="base" title="基础信息">
              <p className="text-sm text-muted-foreground">锚点 rail 适合设置页、健康页等重内容页面。</p>
            </AnchorSectionBlock>
            <AnchorSectionBlock id="state" title="状态与反馈">
              <p className="text-sm text-muted-foreground">点击左侧锚点会滚动到对应分区。</p>
            </AnchorSectionBlock>
            <AnchorSectionBlock id="data" title="数据展示">
              <p className="text-sm text-muted-foreground">分区块只做轻分隔，不嵌套卡片。</p>
            </AnchorSectionBlock>
          </AnchorRailLayout>
        </TabsContent>
        <TabsContent value="scroll" className="pt-3">
          <ScrollArea className="h-32 rounded-md border p-3">
            <div className="space-y-2 text-sm">
              {Array.from({ length: 8 }).map((_, index) => (
                <div key={index} className="flex items-center justify-between">
                  <span>同步日志 #{index + 1}</span>
                  <span className="font-mono text-muted-foreground">10.24.8.{index + 10}</span>
                </div>
              ))}
            </div>
          </ScrollArea>
        </TabsContent>
      </Tabs>
    </ComponentShell>
  )
}

function FeedbackPreview() {
  return (
    <ComponentShell>
      <div className="grid gap-4 md:grid-cols-3">
        <AsyncSection isLoading skeleton={<Skeleton className="h-16 rounded-lg" />}>
          <span>完成</span>
        </AsyncSection>
        <AsyncSection isLoading={false} isError error={new Error('目标服务器不可达')}>
          <span>完成</span>
        </AsyncSection>
        <div className="space-y-2">
          <Button onClick={() => toast.success('同步任务已加入队列')}>触发 Toaster</Button>
          <Toaster richColors position="top-right" />
        </div>
        <TableSkeleton columns={3} rows={2} />
        <CardGridSkeleton count={2} gridClass="grid grid-cols-2 gap-2" />
        <TileGridSkeleton count={2} />
      </div>
    </ComponentShell>
  )
}

function DashboardPreview() {
  return (
    <ComponentShell>
      {/* B 版 KPI 卡：图标角标 + 26px 大数值 + 趋势 + 底部可视化槽 */}
      <div className="mb-4 grid gap-3 sm:grid-cols-3">
        <KpiCard
          label="可调度服务器"
          value="10"
          unit=" / 19 台"
          icon={<Server className="size-4" />}
          tone="brand"
          visual={
            <div className="h-1.5 overflow-hidden rounded-full bg-muted">
              <div className="h-full rounded-full bg-brand" style={{ width: '52.6%' }} />
            </div>
          }
          meta={
            <>
              健康可接量
              <span className="font-semibold text-ok">稳定</span>
            </>
          }
        />
        <KpiCard
          label="在线玩家"
          value="440"
          icon={<Users className="size-4" />}
          tone="ok"
          meta={
            <span className="inline-flex items-center gap-0.5 font-semibold text-ok">
              <TrendingUp className="size-3" />
              6.8%
            </span>
          }
        />
        <KpiCard
          label="平均 TPS"
          value="19.4"
          icon={<Gauge className="size-4" />}
          tone="warn"
          meta={<span className="text-warn">2 台 &lt; 18</span>}
        />
      </div>
      <div className="grid gap-4 md:grid-cols-2">
        <div className="grid place-items-center rounded-md border p-4">
          <GaugeRing
            icon={<Database className="size-5" />}
            ratio={0.66}
            level="warn"
            label="连接池"
            valueText="66 / 100"
            hint="66%"
          />
        </div>
        <div className="space-y-4">
          <HealthBar segments={healthSegments} />
          <div className="grid grid-cols-2 gap-3">
            <IconStat icon={<Server className="size-4" />} label="在线子服" value="742" level="ok" />
            <IconStat icon={<Gauge className="size-4" />} label="平均 TPS" value="19.8" level="ok" />
          </div>
          <div className="h-14 rounded-md border p-2">
            <MiniSparkline values={[17.8, 18.4, 19.1, 19.6, 19.8, 19.4]} color="var(--primary)" />
          </div>
        </div>
      </div>
    </ComponentShell>
  )
}

function TextPreview() {
  return (
    <ComponentShell>
      <div className="space-y-4">
        <SectionHeader icon={<ListChecks className="size-4" />} title="发布说明" count="MarkdownLite" />
        <MarkdownLite
          source={[
            '## 同步前检查',
            '- **增量比对**：只传输哈希变化文件',
            '- **本地备份**：覆盖前保留时间戳目录',
            '',
            '### 风险提示',
            '失败率超过阈值会自动熔断。',
          ].join('\n')}
          className="rounded-md border p-3 text-sm"
        />
      </div>
    </ComponentShell>
  )
}

function UtilityPreview() {
  const level = ratioLevel(0.82)
  return (
    <ComponentShell>
      <div className="space-y-3 text-sm">
        <div className="flex flex-wrap gap-2">
          <span className={cn(badgeVariants({ variant: 'outline' }), 'font-mono')}>cn</span>
          <span className={buttonVariants({ variant: 'outline', size: 'sm' })}>buttonVariants</span>
          <span className={cn(tabsListVariants({ variant: 'line' }), 'h-8 px-2')}>tabsListVariants</span>
          <span className={cn('rounded-md px-2 py-1', level === 'warn' && 'bg-amber-500/10 text-amber-700')}>
            ratioLevel(0.82) = {level}
          </span>
        </div>
        <Separator />
        <p className="text-muted-foreground">
          类型导出与样式函数在覆盖清单中展示，新增导出必须同步补充本博物馆。
        </p>
      </div>
    </ComponentShell>
  )
}

const items: MuseumItem[] = [
  {
    id: 'buttons',
    title: '按钮与徽标',
    group: '基础控件',
    description: '覆盖常用按钮 variant、size、禁用态和 Badge 状态呈现。',
    exports: ['Button', 'buttonVariants', 'Badge', 'badgeVariants'],
    states: ['default', 'secondary', 'outline', 'ghost', 'destructive', 'link', 'disabled', 'ok', 'warn', 'crit', 'off', 'brand'],
    preview: <ButtonPreview />,
  },
  {
    id: 'forms',
    title: '表单输入',
    group: '基础控件',
    description: '展示输入、文本域、复选框、下拉、可筛选组合框与标签。',
    exports: [
      'Input',
      'Textarea',
      'Checkbox',
      'Combobox',
      'ComboboxOption',
      'ComboboxProps',
      'Select',
      'SelectTrigger',
      'SelectValue',
      'SelectContent',
      'SelectItem',
      'SelectGroup',
      'SelectLabel',
      'SelectSeparator',
      'SelectScrollUpButton',
      'SelectScrollDownButton',
      'Label',
    ],
    states: ['value', 'placeholder', 'clearable', 'checked', 'strict select'],
    preview: <FormPreview />,
  },
  {
    id: 'overlays',
    title: '弹层与菜单',
    group: '交互反馈',
    description: '展示 Dialog、Sheet、AlertDialog、DropdownMenu、Tooltip 和高摩擦确认。',
    exports: [
      'Dialog',
      'DialogTrigger',
      'DialogContent',
      'DialogHeader',
      'DialogFooter',
      'DialogTitle',
      'DialogDescription',
      'DialogClose',
      'DialogOverlay',
      'DialogPortal',
      'Sheet',
      'SheetTrigger',
      'SheetContent',
      'SheetHeader',
      'SheetFooter',
      'SheetTitle',
      'SheetDescription',
      'SheetClose',
      'AlertDialog',
      'AlertDialogTrigger',
      'AlertDialogContent',
      'AlertDialogHeader',
      'AlertDialogFooter',
      'AlertDialogTitle',
      'AlertDialogDescription',
      'AlertDialogCancel',
      'AlertDialogAction',
      'AlertDialogMedia',
      'AlertDialogOverlay',
      'AlertDialogPortal',
      'DestructiveConfirmDialog',
      'DropdownMenu',
      'DropdownMenuTrigger',
      'DropdownMenuContent',
      'DropdownMenuItem',
      'DropdownMenuLabel',
      'DropdownMenuSeparator',
      'DropdownMenuGroup',
      'DropdownMenuCheckboxItem',
      'DropdownMenuRadioGroup',
      'DropdownMenuRadioItem',
      'DropdownMenuPortal',
      'DropdownMenuShortcut',
      'DropdownMenuSub',
      'DropdownMenuSubTrigger',
      'DropdownMenuSubContent',
      'Tooltip',
      'TooltipProvider',
      'TooltipTrigger',
      'TooltipContent',
    ],
    states: ['open', 'closed', 'confirm', 'destructive', 'hover'],
    preview: <OverlayPreview />,
  },
  {
    id: 'data',
    title: '数据展示',
    group: '展示组件',
    description: '展示汇总指标条和列定义驱动表格。',
    exports: ['SummaryStrip', 'SummaryItem', 'SummaryTone', 'DataTable', 'DataTableColumn', 'Table', 'TableHeader', 'TableBody', 'TableRow', 'TableHead', 'TableCell'],
    states: ['empty', 'paged', 'row click', 'semantic tone'],
    preview: <DataPreview />,
  },
  {
    id: 'layout',
    title: '布局容器',
    group: '展示组件',
    description: '展示卡片、Tabs、锚点 rail、滚动区与分隔线。',
    exports: [
      'Card',
      'CardHeader',
      'CardContent',
      'CardFooter',
      'CardTitle',
      'CardDescription',
      'CardAction',
      'Tabs',
      'TabsList',
      'TabsTrigger',
      'TabsContent',
      'tabsListVariants',
      'AnchorRailLayout',
      'AnchorSectionBlock',
      'AnchorSection',
      'ScrollArea',
      'ScrollBar',
      'Separator',
    ],
    states: ['default tab', 'sticky rail', 'scroll content'],
    preview: <LayoutPreview />,
  },
  {
    id: 'feedback',
    title: '加载与反馈',
    group: '交互反馈',
    description: '展示异步三态、骨架屏、Toast 容器和常用骨架组合。',
    exports: ['AsyncSection', 'Skeleton', 'TableSkeleton', 'CardGridSkeleton', 'TileGridSkeleton', 'Toaster'],
    states: ['loading', 'error', 'toast', 'table skeleton', 'card skeleton'],
    preview: <FeedbackPreview />,
  },
  {
    id: 'dashboard',
    title: '仪表盘展示',
    group: '可视化',
    description: '展示运维台常用 KPI、健康条、进度环和迷你趋势。',
    exports: [
      'GaugeRing',
      'HealthBar',
      'HealthSegment',
      'IconStat',
      'KpiCard',
      'KpiTone',
      'KpiTrend',
      'MiniSparkline',
      'HealthLevel',
      'levelSolid',
      'levelText',
      'levelSoft',
      'levelRing',
      'statusLevel',
      'ratioLevel',
      'countLevel',
    ],
    states: ['ok', 'warn', 'danger', 'muted', 'empty sparkline'],
    preview: <DashboardPreview />,
  },
  {
    id: 'page-header',
    title: '二阶页眉',
    group: '展示组件',
    description: '业务页顶栏：图标徽章 + 标题/副文案 + 右侧操作槽；区段标题 base/lg 对照。',
    exports: ['PageHeader', 'SectionHeader'],
    states: ['with actions', 'with description', 'section base', 'section lg'],
    preview: <PageHeaderPreview />,
  },
  {
    id: 'text',
    title: '文本与区段',
    group: '展示组件',
    description: '展示区段标题和轻量 Markdown 渲染。',
    exports: ['MarkdownLite'],
    states: ['heading', 'list', 'bold'],
    preview: <TextPreview />,
  },
  {
    id: 'utilities',
    title: '工具导出',
    group: '工具',
    description: '展示类名合并、variant 函数和健康等级工具的使用方式。',
    exports: ['cn', 'buttonVariants', 'badgeVariants', 'tabsListVariants'],
    states: ['class merge', 'variant class', 'health helpers'],
    preview: <UtilityPreview />,
  },
]

const groups = Array.from(new Set(items.map((item) => item.group)))
const covered = new Set(UI_WIKI_COVERED_EXPORTS)

function App() {
  const [query, setQuery] = useState('')
  const [activeId, setActiveId] = useState(items[0].id)
  const filtered = useMemo(() => {
    const keyword = query.trim().toLowerCase()
    if (!keyword) return items
    return items.filter((item) =>
      [item.title, item.group, item.description, ...item.exports, ...item.states]
        .join(' ')
        .toLowerCase()
        .includes(keyword),
    )
  }, [query])
  const visibleItems = filtered.length > 0 ? filtered : items
  const active = visibleItems.find((item) => item.id === activeId) ?? visibleItems[0]

  return (
    <div className="flex h-screen bg-background text-foreground">
      <aside className="flex w-80 shrink-0 flex-col border-r bg-sidebar">
        <div className="border-b p-4">
          <div className="flex items-center gap-2">
            <div className="grid size-8 place-items-center rounded-md bg-primary text-primary-foreground">
              <Boxes className="size-4" />
            </div>
            <div>
              <h1 className="text-base font-semibold">Beacon UI 控件博物馆</h1>
              <p className="text-xs text-muted-foreground">
                已覆盖 {covered.size} 个 UI 包导出
              </p>
            </div>
          </div>
          <div className="relative mt-4">
            <Search className="absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground" />
            <Input
              value={query}
              onChange={(event) => { setQuery(event.target.value); }}
              placeholder="搜索组件、状态或导出名"
              className="pl-8"
            />
          </div>
        </div>
        <ScrollArea className="min-h-0 flex-1">
          <div className="space-y-4 p-3">
            {groups.map((group) => {
              const groupItems = filtered.filter((item) => item.group === group)
              if (groupItems.length === 0) return null
              return (
                <div key={group}>
                  <div className="px-2 py-1 text-xs font-medium text-muted-foreground">{group}</div>
                  <div className="space-y-1">
                    {groupItems.map((item) => (
                      <button
                        key={item.id}
                        type="button"
                        onClick={() => { setActiveId(item.id); }}
                        className={cn(
                          'flex w-full items-center justify-between rounded-md px-2 py-2 text-left text-sm transition-colors',
                          active.id === item.id
                            ? 'bg-sidebar-accent text-sidebar-accent-foreground'
                            : 'hover:bg-sidebar-accent/60',
                        )}
                      >
                        <span>{item.title}</span>
                        <Badge variant="outline">{item.exports.length}</Badge>
                      </button>
                    ))}
                  </div>
                </div>
              )
            })}
          </div>
        </ScrollArea>
      </aside>
      <main className="min-w-0 flex-1 overflow-y-auto">
        <div className="border-b px-6 py-4">
          <div className="flex flex-wrap items-center gap-2">
            <Badge variant="secondary">{active.group}</Badge>
            {active.states.map((state) => (
              <Badge key={state} variant="outline">
                {state}
              </Badge>
            ))}
          </div>
          <div className="mt-3 flex min-w-0 items-start gap-4">
            <div className="min-w-0 flex-1">
              <h2 className="text-2xl font-semibold">{active.title}</h2>
              <p className="mt-1 text-sm text-muted-foreground">{active.description}</p>
            </div>
            <Button
              variant="outline"
              onClick={() => toast.info(`已定位 ${active.title}`)}
            >
              <Zap />
              试用状态
            </Button>
          </div>
        </div>
        <div className="grid gap-6 p-6">
          <section className="grid gap-3">
            <SectionHeader icon={<Activity className="size-4" />} title="实时示例" />
            {active.preview}
          </section>
          <section className="grid gap-3">
            <SectionHeader icon={<ArchiveRestore className="size-4" />} title="覆盖导出" count={`${String(active.exports.length)} 项`} />
            <ExportList names={active.exports} />
          </section>
          <section className="grid gap-3">
            <SectionHeader icon={<ShieldAlert className="size-4" />} title="覆盖状态" />
            <div className="grid gap-2 rounded-lg border p-4 text-sm md:grid-cols-3">
              <div className="flex items-center gap-2">
                <CheckCircle2 className="size-4 text-green-600" />
                基础用法
              </div>
              <div className="flex items-center gap-2">
                <SlidersHorizontal className="size-4 text-primary" />
                主要变体 / 尺寸
              </div>
              <div className="flex items-center gap-2">
                <AlertTriangle className="size-4 text-amber-500" />
                状态 / 空态 / 错误态
              </div>
            </div>
          </section>
          <section className="grid gap-3">
            <SectionHeader icon={<Terminal className="size-4" />} title="导出覆盖审计" />
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>指标</TableHead>
                  <TableHead>结果</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                <TableRow>
                  <TableCell>UI 包公开导出</TableCell>
                  <TableCell>{UI_WIKI_COVERED_EXPORTS.length}</TableCell>
                </TableRow>
                <TableRow>
                  <TableCell>当前条目覆盖</TableCell>
                  <TableCell>{active.exports.length}</TableCell>
                </TableRow>
              </TableBody>
            </Table>
          </section>
        </div>
      </main>
    </div>
  )
}

export default App
