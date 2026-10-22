import moderationUsers from './moderationUsers'
import groupCacheTest from './groupCacheTest'
import requestCapture from './requestCapture'
import astraGateway from './astraGateway'
import overview from './overview'
import channels from './channels'
import accounts from './accounts'
import resources from './resources'
import ops from './ops'
import settings from './settings'
import audit from './audit'
import promptAudit from './promptAudit'
import safetyPromptAudit from './safetyPromptAudit'
import plugins from './plugins'
import harvestFlow from './harvestFlow'

export default {
  ...moderationUsers,
  ...groupCacheTest,
  ...requestCapture,
  ...astraGateway,
  ...overview,
  ...channels,
  ...accounts,
  ...resources,
  ...ops,
  ...settings,
  ...audit,
  ...promptAudit,
  ...safetyPromptAudit,
  ...plugins,
  ...harvestFlow,
}
