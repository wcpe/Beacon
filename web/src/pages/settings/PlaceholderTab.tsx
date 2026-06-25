// 空壳子 tab 占位（FR-94）：仅渲染占位文案，真实内容由后续 FR 填。
import { Card, CardContent } from '@/components/ui/card'

export default function PlaceholderTab({ text }: { text: string }) {
  return (
    <Card>
      <CardContent className="py-10 text-center text-sm text-muted-foreground">{text}</CardContent>
    </Card>
  )
}
