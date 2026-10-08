// 交付申请 → 统一审批的域间交接（mock 专用）：交付域的六类申请动作只在此登记「申请了什么」，
// 审批读模型的行仍由 approval 域建（审批真源只有一处）。
//
// 为什么单列一个模块：approval 域已 import delivery 域（审批通过后回投领域副作用），
// 若交付域再直接 import approval 域即形成**循环依赖**（项目规则明令禁止）。
// 本模块不 import 任何域，依赖方向单向：delivery → bridge ← approval。

import { defineScenarioStore } from '../store'

/** 一次交付申请的登记项（requestId 由本模块分配，保证票据与审批行同号） */
export interface DeliveryApprovalSpec {
  requestId: string
  /** 后端 authz 操作键（delivery.approve / delivery.resume / delivery.confirm_batch / …） */
  operationKey: string
  orderId: number
  namespaceId: number
  reason: string
  safeSummary: string
  /** 继续灰度的恢复方式（仅 delivery.resume 用；审批通过后按它重放领域副作用） */
  resumeMode?: 'retry_failed' | 'skip_failed'
  /** 确认放行的批号（仅 delivery.confirm_batch 用） */
  batchNo?: number
  /** 目标级（子集）回滚的选中目标（仅 delivery.rollback 且子集路径用，FR-270） */
  serverIds?: string[]
}

interface BridgeState {
  specs: DeliveryApprovalSpec[]
  seq: number
}

const getBridgeState: () => BridgeState = defineScenarioStore(() => ({ specs: [], seq: 0 }))

/** 登记一次交付申请并分配申请号（票据与审批行同号） */
export function registerDeliveryApproval(
  spec: Omit<DeliveryApprovalSpec, 'requestId'>,
): DeliveryApprovalSpec {
  const state = getBridgeState()
  state.seq += 1
  const registered: DeliveryApprovalSpec = {
    ...spec,
    requestId: `apr_change_${String(spec.orderId)}_${String(state.seq)}`,
  }
  state.specs.push(registered)
  return registered
}

/** 已登记的申请清单（approval 域据此建行；delivery 域据此在审批通过后重放领域副作用） */
export function deliveryApprovalSpecs(): DeliveryApprovalSpec[] {
  return getBridgeState().specs
}
