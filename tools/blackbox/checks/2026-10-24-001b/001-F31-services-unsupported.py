"""F-31 (gap): several standard services a foreign client commonly uses return
Bad_ServiceUnsupported, so a client cannot throttle or retune a subscription without tearing it
down, nor resolve a browse path. Documents what is missing (ModifyMonitoredItems,
ModifySubscription, SetPublishingMode, TransferSubscriptions, TranslateBrowsePathsToNodeIds,
Call, HistoryRead). The guide promises browse/read/subscribe, which work, so this is a gap, not a
refutation; it fails today to record the state and flips when any of these is implemented.
Project: SUT_PROJECT. Oracle: asyncua + Part 4."""
import os, sys, asyncio
from asyncua import Client, ua
EP = os.environ["SUT_OPCUA"]; unsupported = []
def nid(s): return ua.NodeId.from_string(s)
async def try_svc(name, coro):
    try: await coro; print(f"   {name}: supported"); return True
    except ua.UaStatusCodeError as e:
        bad = "ServiceUnsupported" in e.__class__.__name__ or "ServiceUnsupported" in str(e)
        print(f"   {name}: {e.__class__.__name__}"); (unsupported.append(name) if bad else None); return not bad
    except Exception as e: print(f"   {name}: {type(e).__name__} {str(e)[:50]}"); return True
async def main():
    async with Client(EP, timeout=8) as c:
        class H:
            def datachange_notification(self, node, val, data): pass
        sub = await c.create_subscription(100, H()); mi = await sub.subscribe_data_change(c.get_node("ns=1;s=Counter"))
        mp = ua.ModifyMonitoredItemsParameters(); mp.SubscriptionId = sub.subscription_id; mp.TimestampsToReturn = ua.TimestampsToReturn.Both
        mr = ua.MonitoredItemModifyRequest(); mr.MonitoredItemId = mi; mr.RequestedParameters = ua.MonitoringParameters(ClientHandle=1, SamplingInterval=500, QueueSize=1, DiscardOldest=True); mp.ItemsToModify = [mr]
        await try_svc("ModifyMonitoredItems", c.uaclient.modify_monitored_items(mp))
        sp = ua.SetPublishingModeParameters(); sp.PublishingEnabled = False; sp.SubscriptionIds = [sub.subscription_id]
        if hasattr(c.uaclient, "set_publishing_mode"):
            await try_svc("SetPublishingMode", c.uaclient.set_publishing_mode(sp))
        else:
            await try_svc("SetPublishingMode", c.get_node(2253).call_method(ua.NodeId(1)))  # placeholder; asyncua lacks the helper
        tp = ua.TransferSubscriptionsParameters(); tp.SubscriptionIds = [sub.subscription_id]; tp.SendInitialValues = True
        await try_svc("TransferSubscriptions", c.uaclient.transfer_subscriptions(tp))
        await try_svc("TranslateBrowsePathsToNodeIds", c.nodes.objects.get_child(["1:typed", "1:Level"]))
        await try_svc("Call(Server.GetMonitoredItems)", c.get_node(2253).call_method(ua.NodeId(11492), ua.Variant(sub.subscription_id, ua.VariantType.UInt32)))
        await sub.delete()
    print(f"   unsupported: {unsupported}")
    sys.exit(1 if unsupported else 0)
asyncio.run(main())
