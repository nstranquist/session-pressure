import Foundation
import NDevPressureCore

let delegate = TraceListenerDelegate()
let listener = NSXPCListener(machServiceName: NDevPressureTraceContract.helperLabel)
listener.delegate = delegate
listener.resume()
RunLoop.current.run()
